package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

var (
	ErrEnterpriseDeviceUnavailable = errors.New("enterprise device is unavailable")
	ErrEnterpriseDeviceScope       = errors.New("invalid enterprise device assignment scope")
)

type EnterpriseDeviceAssignmentInput struct {
	OrganizationID uint
	DeviceID       uint
	DepartmentID   *uint
	UserID         *uint
	TaskID         *uint
	AllowedTools   []string
	Classification string
	AssignedBy     uint
	ExpiresAt      *time.Time
}

func AssignEnterpriseDevice(db *gorm.DB, input EnterpriseDeviceAssignmentInput) (model.EnterpriseDeviceAssignment, error) {
	if db == nil || input.OrganizationID == 0 || input.DeviceID == 0 || input.AssignedBy == 0 {
		return model.EnterpriseDeviceAssignment{}, ErrEnterpriseDeviceScope
	}
	scopeType := deviceAssignmentScope(input.DepartmentID, input.UserID, input.TaskID)
	if !model.DeviceAssignmentScopeValid(scopeType, input.DepartmentID, input.UserID, input.TaskID) {
		return model.EnterpriseDeviceAssignment{}, ErrEnterpriseDeviceScope
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return model.EnterpriseDeviceAssignment{}, ErrEnterpriseDeviceScope
	}
	tools, err := normalizedDeviceTools(input.AllowedTools)
	if err != nil {
		return model.EnterpriseDeviceAssignment{}, err
	}
	data, _ := json.Marshal(tools)
	assignment := model.EnterpriseDeviceAssignment{}
	err = db.Transaction(func(tx *gorm.DB) error {
		var device model.EnterpriseDevice
		if err := tx.Where("id = ? AND organization_id = ? AND status = ?", input.DeviceID, input.OrganizationID, model.EnterpriseDeviceStatusActive).First(&device).Error; err != nil {
			return ErrEnterpriseDeviceUnavailable
		}
		if input.DepartmentID != nil {
			var department model.Department
			if err := tx.Where("id = ? AND organization_id = ?", *input.DepartmentID, input.OrganizationID).First(&department).Error; err != nil {
				return ErrEnterpriseDeviceScope
			}
		}
		if input.UserID != nil {
			var member model.OrganizationMember
			if err := tx.Where("organization_id = ? AND user_id = ? AND status = ?", input.OrganizationID, *input.UserID, model.OrganizationMemberStatusActive).First(&member).Error; err != nil {
				return ErrEnterpriseDeviceScope
			}
		}
		if input.TaskID != nil {
			var task model.EnterpriseTask
			if err := tx.Where("id = ? AND organization_id = ?", *input.TaskID, input.OrganizationID).First(&task).Error; err != nil {
				return ErrEnterpriseDeviceScope
			}
		}
		assignment = model.EnterpriseDeviceAssignment{OrganizationID: input.OrganizationID, DeviceID: input.DeviceID, ScopeType: scopeType, DepartmentID: input.DepartmentID, UserID: input.UserID, TaskID: input.TaskID, AllowedTools: string(data), Classification: strings.TrimSpace(input.Classification), Status: model.EnterpriseDeviceAssignmentActive, AssignedBy: input.AssignedBy, ExpiresAt: input.ExpiresAt}
		return tx.Create(&assignment).Error
	})
	return assignment, err
}

func RevokeEnterpriseDeviceAssignment(db *gorm.DB, organizationID, assignmentID, actorID uint) error {
	if db == nil || organizationID == 0 || assignmentID == 0 || actorID == 0 {
		return ErrEnterpriseDeviceScope
	}
	result := db.Model(&model.EnterpriseDeviceAssignment{}).Where("id = ? AND organization_id = ? AND status = ?", assignmentID, organizationID, model.EnterpriseDeviceAssignmentActive).Update("status", model.EnterpriseDeviceAssignmentRevoked)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrEnterpriseDeviceUnavailable
	}
	return nil
}

// ActiveEnterpriseDeviceAssignments returns only assignments matching the
// current task, employee, or department and excludes expired or revoked grants.
func ActiveEnterpriseDeviceAssignments(db *gorm.DB, organizationID, userID uint, departmentIDs []uint, taskID *uint) ([]model.EnterpriseDeviceAssignment, error) {
	if db == nil || organizationID == 0 || userID == 0 {
		return nil, ErrEnterpriseDeviceScope
	}
	query := db.Where("organization_id = ? AND status = ? AND deleted_at IS NULL", organizationID, model.EnterpriseDeviceAssignmentActive).Where("expires_at IS NULL OR expires_at > ?", time.Now())
	conditions := []string{"(scope_type = ? AND user_id = ?)"}
	args := []interface{}{model.EnterpriseDeviceAssignmentUser, userID}
	if taskID != nil && *taskID != 0 {
		conditions = append(conditions, "(scope_type = ? AND task_id = ?)")
		args = append(args, model.EnterpriseDeviceAssignmentTask, *taskID)
	}
	if len(departmentIDs) > 0 {
		conditions = append(conditions, "(scope_type = ? AND department_id IN ?)")
		args = append(args, model.EnterpriseDeviceAssignmentDepartment, departmentIDs)
	}
	var assignments []model.EnterpriseDeviceAssignment
	err := query.Where(strings.Join(conditions, " OR "), args...).Order("id ASC").Find(&assignments).Error
	return assignments, err
}

func deviceAssignmentScope(departmentID, userID, taskID *uint) string {
	if departmentID != nil {
		return model.EnterpriseDeviceAssignmentDepartment
	}
	if userID != nil {
		return model.EnterpriseDeviceAssignmentUser
	}
	if taskID != nil {
		return model.EnterpriseDeviceAssignmentTask
	}
	return ""
}

func normalizedDeviceTools(tools []string) ([]string, error) {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(tools))
	for _, raw := range tools {
		tool := strings.TrimSpace(raw)
		if tool == "" {
			continue
		}
		if len(tool) > 120 {
			return nil, ErrEnterpriseDeviceScope
		}
		if _, exists := seen[tool]; exists {
			continue
		}
		seen[tool] = struct{}{}
		result = append(result, tool)
	}
	return result, nil
}
