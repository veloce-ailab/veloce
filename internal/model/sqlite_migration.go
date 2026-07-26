package model

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const sqliteMigrationBatchSize = 500

var (
	sqliteMigrationModelsMu         sync.RWMutex
	registeredSQLiteMigrationModels []interface{}
)

// RegisterSQLiteMigrationModels lets feature packages register their own
// persistent models for the standalone SQLite-to-server migration command.
// Those packages cannot be imported here without creating import cycles.
func RegisterSQLiteMigrationModels(models ...interface{}) {
	sqliteMigrationModelsMu.Lock()
	defer sqliteMigrationModelsMu.Unlock()
	registeredSQLiteMigrationModels = append(registeredSQLiteMigrationModels, models...)
}

// SQLiteMigrationReport describes a completed one-way SQLite export.
type SQLiteMigrationReport struct {
	TargetDriver  string
	Tables        int
	Rows          int64
	DiscardedRows int64
	RepairedRows  int64
}

// MigrateSQLiteToTarget copies all application tables from sourcePath to an
// empty MySQL or PostgreSQL database. It never modifies the SQLite source.
func MigrateSQLiteToTarget(sourcePath, targetDriver, targetDSN string) (SQLiteMigrationReport, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	targetDriver = strings.ToLower(strings.TrimSpace(targetDriver))
	targetDSN = strings.TrimSpace(targetDSN)
	if sourcePath == "" {
		return SQLiteMigrationReport{}, errors.New("SQLite source path is required")
	}
	if targetDSN == "" {
		return SQLiteMigrationReport{}, errors.New("target DB_DSN is required")
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("inspect SQLite source: %w", err)
	}
	if info.IsDir() {
		return SQLiteMigrationReport{}, fmt.Errorf("SQLite source path %q is a directory", sourcePath)
	}
	targetDriver, targetDialector, err := migrationTargetDialector(targetDriver, targetDSN)
	if err != nil {
		return SQLiteMigrationReport{}, err
	}

	source, err := gorm.Open(sqlite.Open(sqliteDSN(sourcePath)), &gorm.Config{})
	if err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("open SQLite source: %w", err)
	}
	sourceSQL, err := source.DB()
	if err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("access SQLite source: %w", err)
	}
	defer sourceSQL.Close()
	if err := configureDatabaseConnection(sourceSQL, true); err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("configure SQLite source: %w", err)
	}
	if err := sourceSQL.Ping(); err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("ping SQLite source: %w", err)
	}

	// Create the target without foreign keys first, so records can be copied in
	// batches without requiring a fragile ordering across every relationship.
	target, err := gorm.Open(targetDialector, &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("open %s target: %w", targetDriver, err)
	}
	targetSQL, err := target.DB()
	if err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("access %s target: %w", targetDriver, err)
	}
	defer targetSQL.Close()
	if err := configureDatabaseConnection(targetSQL, false); err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("configure %s target: %w", targetDriver, err)
	}
	if err := targetSQL.Ping(); err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("ping %s target: %w", targetDriver, err)
	}

	models := sqliteMigrationModels()
	for _, item := range models {
		if target.Migrator().HasTable(item) {
			return SQLiteMigrationReport{}, fmt.Errorf("target database is not empty: table %q already exists", migrationModelTableName(target, item))
		}
	}
	if err := target.AutoMigrate(models...); err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("create %s schema: %w", targetDriver, err)
	}

	report := SQLiteMigrationReport{TargetDriver: targetDriver}
	if err := target.Transaction(func(tx *gorm.DB) error {
		for _, item := range models {
			if !source.Migrator().HasTable(item) {
				continue
			}
			rows, err := copySQLiteMigrationTable(source, tx, item)
			if err != nil {
				return err
			}
			report.Tables++
			report.Rows += rows
		}
		return nil
	}); err != nil {
		return SQLiteMigrationReport{}, err
	}
	discarded, err := discardDanglingModelConfigs(target)
	if err != nil {
		return SQLiteMigrationReport{}, err
	}
	report.DiscardedRows = discarded
	repaired, err := repairDanglingReferences(target, models)
	if err != nil {
		return SQLiteMigrationReport{}, err
	}
	report.RepairedRows = repaired

	// The copied data now satisfies the source's relationships. Re-run schema
	// migration with constraints enabled so the target matches normal startup.
	targetWithConstraints, err := gorm.Open(targetDialector, &gorm.Config{})
	if err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("reopen %s target: %w", targetDriver, err)
	}
	if err := targetWithConstraints.AutoMigrate(models...); err != nil {
		return SQLiteMigrationReport{}, fmt.Errorf("finalize %s schema: %w", targetDriver, err)
	}
	return report, nil
}

// discardDanglingModelConfigs removes legacy records that cannot be used by
// the application because their channel or model was already deleted. SQLite
// can retain these rows when foreign keys were disabled; PostgreSQL/MySQL
// correctly reject them when the constraint is created.
func discardDanglingModelConfigs(db *gorm.DB) (int64, error) {
	if !db.Migrator().HasTable(&ModelConfig{}) {
		return 0, nil
	}
	result := db.Exec(`DELETE FROM model_configs
WHERE NOT EXISTS (SELECT 1 FROM channels WHERE channels.id = model_configs.channel_id)
   OR NOT EXISTS (SELECT 1 FROM models WHERE models.id = model_configs.model_id)`)
	if result.Error != nil {
		return 0, fmt.Errorf("discard dangling model configurations: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// repairDanglingReferences makes legacy SQLite data compatible with the target
// database's foreign keys. Nullable references are cleared to retain logs and
// history; rows with a required missing parent are removed.
func repairDanglingReferences(db *gorm.DB, models []interface{}) (int64, error) {
	cache := &sync.Map{}
	seen := map[string]struct{}{}
	var repaired int64
	for _, item := range models {
		parsed, err := schema.Parse(item, cache, db.NamingStrategy)
		if err != nil {
			return 0, fmt.Errorf("parse migration model relationship: %w", err)
		}
		for _, relationship := range parsed.Relationships.Relations {
			constraint := relationship.ParseConstraint()
			if constraint == nil || constraint.Schema == nil || constraint.ReferenceSchema == nil || len(constraint.ForeignKeys) == 0 || len(constraint.ForeignKeys) != len(constraint.References) {
				continue
			}
			key := danglingReferenceKey(constraint)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			if !db.Migrator().HasTable(constraint.Schema.Table) || !db.Migrator().HasTable(constraint.ReferenceSchema.Table) {
				continue
			}
			where := danglingReferenceWhere(constraint)
			if where == "" {
				continue
			}
			if danglingReferenceIsRequired(constraint) {
				result := db.Exec("DELETE FROM " + constraint.Schema.Table + " WHERE " + where)
				if result.Error != nil {
					return 0, fmt.Errorf("remove dangling records for %s: %w", constraint.Name, result.Error)
				}
				repaired += result.RowsAffected
				continue
			}
			columns := make([]string, 0, len(constraint.ForeignKeys))
			for _, foreignKey := range constraint.ForeignKeys {
				columns = append(columns, foreignKey.DBName+" = NULL")
			}
			result := db.Exec("UPDATE " + constraint.Schema.Table + " SET " + strings.Join(columns, ", ") + " WHERE " + where)
			if result.Error != nil {
				return 0, fmt.Errorf("clear dangling references for %s: %w", constraint.Name, result.Error)
			}
			repaired += result.RowsAffected
		}
	}
	return repaired, nil
}

func danglingReferenceKey(constraint *schema.Constraint) string {
	parts := []string{constraint.Schema.Table, constraint.ReferenceSchema.Table}
	for index, foreignKey := range constraint.ForeignKeys {
		parts = append(parts, foreignKey.DBName+"="+constraint.References[index].DBName)
	}
	return strings.Join(parts, ":")
}

func danglingReferenceWhere(constraint *schema.Constraint) string {
	nonNull := make([]string, 0, len(constraint.ForeignKeys))
	matches := make([]string, 0, len(constraint.ForeignKeys))
	for index, foreignKey := range constraint.ForeignKeys {
		nonNull = append(nonNull, constraint.Schema.Table+"."+foreignKey.DBName+" IS NOT NULL")
		matches = append(matches, constraint.ReferenceSchema.Table+"."+constraint.References[index].DBName+" = "+constraint.Schema.Table+"."+foreignKey.DBName)
	}
	if len(nonNull) == 0 {
		return ""
	}
	return strings.Join(nonNull, " AND ") + " AND NOT EXISTS (SELECT 1 FROM " + constraint.ReferenceSchema.Table + " WHERE " + strings.Join(matches, " AND ") + ")"
}

func danglingReferenceIsRequired(constraint *schema.Constraint) bool {
	for _, foreignKey := range constraint.ForeignKeys {
		if foreignKey.NotNull {
			return true
		}
	}
	return false
}

func migrationModelTableName(db *gorm.DB, item interface{}) string {
	modelType := reflect.TypeOf(item)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	return db.NamingStrategy.TableName(modelType.Name())
}

func migrationTargetDialector(driver, dsn string) (string, gorm.Dialector, error) {
	if strings.TrimSpace(dsn) == "" {
		return "", nil, errors.New("target DB_DSN is required")
	}
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "postgres", "postgresql":
		return "postgres", postgres.Open(dsn), nil
	case "mysql", "mariadb":
		return "mysql", mysql.Open(dsn), nil
	default:
		return "", nil, errors.New("--migrate requires DB_DRIVER=postgres or DB_DRIVER=mysql")
	}
}

func copySQLiteMigrationTable(source, target *gorm.DB, item interface{}) (int64, error) {
	modelType := reflect.TypeOf(item)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	batchType := reflect.SliceOf(modelType)
	batch := reflect.New(batchType).Interface()
	var copied int64
	err := source.Model(item).FindInBatches(batch, sqliteMigrationBatchSize, func(tx *gorm.DB, _ int) error {
		value := reflect.ValueOf(batch).Elem()
		if value.Len() == 0 {
			return nil
		}
		if err := target.Omit(clause.Associations).Create(value.Interface()).Error; err != nil {
			return fmt.Errorf("copy table %s: %w", migrationModelTableName(source, item), err)
		}
		copied += int64(value.Len())
		return nil
	}).Error
	return copied, err
}

func sqliteMigrationModels() []interface{} {
	models := []interface{}{
		&User{}, &UserAvatar{}, &APIKey{}, &EmailVerificationCode{}, &PhoneVerificationCode{}, &OIDCBindRequest{}, &WebAuthnChallenge{}, &PasskeyCredential{}, &CheckInRecord{}, &PaymentOrder{}, &WalletTransaction{}, &WalletLimitUsage{},
		&Group{}, &UserGroupMembership{}, &ChannelGroupMultiplier{}, &ModelGroupMultiplier{}, &ReferralCommissionLog{}, &UserChannel{}, &UserChannelGroupAccess{}, &UserChannelUserAccess{}, &Channel{}, &Model{}, &ModelConfig{},
		&StatusMonitor{}, &Announcement{}, &SystemSetting{}, &VideoTask{}, &Plugin{}, &UserPluginState{}, &UserPluginConfig{}, &PluginKV{},
		&PersonalCompany{}, &CompanyCharterRevision{}, &PersonalCompanyEmployee{}, &CompanyRoleTemplate{}, &CompanyEmployeeVersion{}, &CompanyCapabilityEvidence{}, &CompanyRecruitmentPlan{}, &CompanyObjective{}, &CompanyWorkItem{}, &CompanyWorkAttempt{}, &CompanyArtifact{}, &CompanyHandoffPackage{}, &CompanyApprovalRequest{}, &CompanyBudgetLedger{}, &CompanyAuditEvent{}, &CompanySignal{}, &CompanyOutboxEvent{},
		&Organization{}, &Department{}, &Workspace{}, &OrganizationMember{}, &WorkspaceMember{}, &Permission{}, &Role{}, &RolePermission{}, &RoleBinding{}, &DepartmentRoleBinding{}, &EnterpriseTask{}, &EnterpriseTaskAssignment{}, &EnterpriseTaskDepartment{}, &DepartmentMember{}, &EnterpriseSharedPool{}, &EnterpriseSharedSession{}, &EnterpriseSharedFile{}, &EnterpriseDevice{}, &EnterpriseDeviceAssignment{}, &QuotaAccount{}, &QuotaLedger{},
	}

	sqliteMigrationModelsMu.RLock()
	deferred := append([]interface{}(nil), registeredSQLiteMigrationModels...)
	sqliteMigrationModelsMu.RUnlock()

	seen := make(map[reflect.Type]struct{}, len(models)+len(deferred))
	for _, item := range models {
		seen[reflect.TypeOf(item)] = struct{}{}
	}
	for _, item := range deferred {
		if item == nil {
			continue
		}
		modelType := reflect.TypeOf(item)
		if _, exists := seen[modelType]; exists {
			continue
		}
		seen[modelType] = struct{}{}
		models = append(models, item)
	}
	return models
}
