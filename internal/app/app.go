package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	webui "github.com/veloce-ailab/veloce-web"
	"github.com/veloce-ailab/veloce/internal/api"
	"github.com/veloce-ailab/veloce/internal/cache"
	messagechannel "github.com/veloce-ailab/veloce/internal/channel"
	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/middleware"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
)

// registerBuiltinFeatures wires the feature modules into the shared startup and
// route registries. These used to live behind an "edition" indirection that was
// always enabled, so they are now registered directly.
func registerBuiltinFeatures() {
	api.EnablePaymentFeature()
	service.RegisterStartupHook(service.InitSubscriptionFeatures)
	service.RegisterStartupHook(service.InitMetaModelFeatures)
	service.RegisterStartupHook(service.InitMemoryFeatures)
	service.RegisterStartupHook(service.InitAdvancedChatFeatures)
	service.RegisterAdminRouteHook(service.RegisterSubscriptionAdminRoutes)
	service.RegisterAdminRouteHook(service.RegisterMetaModelAdminRoutes)
	service.RegisterAdminRouteHook(service.RegisterAdvancedChatAdminRoutes)
	service.RegisterPublicAPIRouteHook(service.RegisterAdvancedChatPublicRoutes)
	service.RegisterUserRouteHook(service.RegisterSubscriptionUserRoutes)
	service.RegisterUserRouteHook(service.RegisterAdvancedChatUserRoutes)
	service.RegisterUserRouteHook(service.RegisterMemoryUserRoutes)
	service.RegisterGeneratedAssetHook(service.ApplyAdvancedChatGeneratedAssetHook)
	service.RegisterMemoryHooks()
}

func Run() error {
	// Initialize config
	config.Init()
	registerBuiltinFeatures()

	// Initialize database
	model.InitDB()
	if err := cache.Connect(context.Background()); err != nil {
		return fmt.Errorf("initialize Redis: %w", err)
	}
	defer func() {
		if err := cache.Close(); err != nil {
			log.Printf("failed to close Redis connection: %v", err)
		}
	}()
	if err := service.RunStartupHooks(); err != nil {
		return err
	}
	// Registers this instance and claims the primary role when it is the first
	// node; later nodes join as replicas and skip scheduled background work.
	if err := service.EnsureCurrentNodeRegistered(); err != nil {
		return err
	}
	service.StartNodeHeartbeat()
	if err := service.InitCommunityAdvancedChatFeatures(); err != nil {
		return err
	}
	if err := service.InitCloudSandboxFeatures(); err != nil {
		return err
	}
	if err := messagechannel.InitFeatures(); err != nil {
		return err
	}

	// Services
	authService, err := service.NewAuthService()
	if err != nil {
		return err
	}
	proxyService := service.NewProxyService()
	syncService := service.NewSyncService()
	statusService := service.NewStatusService()
	reliabilityService := service.NewReliabilityService()
	logCleanupService := service.NewLogCleanupService()
	rateLimiter := middleware.NewRateLimiter()

	// Start sync loop
	syncService.StartSyncLoop()
	statusService.Start()
	reliabilityService.Start()
	logCleanupService.Start()

	// Initialize Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestBodyLimit(maxRequestBodyBytes))
	r.Use(middleware.AuditMiddleware())
	frontendFS, err := webui.DistFS()
	if err != nil {
		return err
	}

	// APIs
	channelAPI := &api.ChannelAPI{SyncService: syncService}
	userChannelAPI := &api.UserChannelAPI{}
	modelAPI := &api.ModelAPI{SyncService: syncService}
	userAPI := &api.UserAPI{AuthService: authService}
	statsAPI := &api.StatsAPI{}
	groupAPI := &api.GroupAPI{}
	systemAPI := &api.SystemAPI{}
	referralAPI := &api.ReferralAPI{}
	statusMonitorAPI := &api.StatusMonitorAPI{StatusService: statusService}
	announcementAPI := &api.AnnouncementAPI{}
	checkInAPI := &api.CheckInAPI{}
	paymentAPI := &api.PaymentAPI{}
	passkeyAPI := &api.PasskeyAPI{AuthService: authService}
	enterpriseAPI := &api.EnterpriseAPI{}
	clusterAPI := &api.ClusterAPI{}

	// Public routes
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	r.GET("/api/public/settings", systemAPI.PublicSettings)
	r.GET("/api/public/models", modelAPI.PublicCatalog)
	r.GET("/api/public/status", statusMonitorAPI.PublicStatus)
	r.GET("/api/public/announcements", announcementAPI.PublicList)
	r.GET("/api/community/categories", proxyCommunityAPI)
	r.GET("/api/community/characters", proxyCommunityAPI)
	r.GET("/api/community/characters/:id", proxyCommunityAPI)
	r.GET("/api/community/knowledge-bases", proxyCommunityAPI)
	r.GET("/api/community/knowledge-bases/:id", proxyCommunityAPI)
	r.GET("/api/community/knowledge-bases/:id/content", proxyCommunityAPI)
	r.GET("/api/community/skills", proxyCommunityAPI)
	r.GET("/api/community/skills/:id", proxyCommunityAPI)
	r.GET("/api/avatars/:id", userAPI.GetAvatar)
	r.GET("/api/pricing", modelAPI.Pricing)
	r.GET("/api/payment/yipay/return", paymentAPI.Return)
	r.GET("/api/payment/yipay/notify", paymentAPI.Notify)
	r.POST("/api/payment/yipay/notify", paymentAPI.Notify)
	r.GET("/api/payment/openpayment/return", paymentAPI.Return)
	r.GET("/api/payment/openpayment/notify", paymentAPI.Notify)
	r.POST("/api/payment/openpayment/notify", paymentAPI.Notify)
	r.GET("/api/payment/openpayment/submit/:order_no", paymentAPI.OpenPaymentSubmit)
	r.POST("/api/payment/wechatpay/notify", paymentAPI.WeChatPayNotify)
	r.POST("/api/payment/alipay/notify", paymentAPI.AlipayNotify)
	r.POST("/api/payment/paypal/notify", paymentAPI.PayPalNotify)
	r.POST("/api/payment/stripe/notify", paymentAPI.StripeNotify)
	r.GET("/api/payment/alipay/return", paymentAPI.OfficialReturn)
	r.GET("/api/payment/paypal/return", paymentAPI.OfficialReturn)
	r.GET("/api/payment/stripe/return", paymentAPI.OfficialReturn)
	r.GET("/api/setup/status", func(c *gin.Context) {
		required, err := authService.InitialSetupRequired()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load setup status"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"required": required})
	})
	r.POST("/api/setup", middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{
		Name:   "initial-setup-ip",
		Limit:  5,
		Window: time.Minute,
	}), func(c *gin.Context) {
		var input struct {
			SiteName string `json:"site_name"`
			Username string `json:"username"`
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		user, token, err := authService.SetupInitialAdmin(service.InitialSetupInput{
			SiteName: input.SiteName,
			Username: input.Username,
			Email:    input.Email,
			Password: input.Password,
		})
		if err != nil {
			service.RecordAuditLog(service.AuditLogInput{
				LogType:    service.AuditLogTypeLogin,
				Action:     "setup_failed",
				Resource:   "initial_setup",
				Method:     c.Request.Method,
				Path:       c.Request.URL.Path,
				StatusCode: http.StatusInternalServerError,
				IPAddress:  c.ClientIP(),
				UserAgent:  c.Request.UserAgent(),
				Message:    err.Error(),
			})
			switch {
			case errors.Is(err, service.ErrInitialSetupComplete):
				c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is already complete"})
			case service.IsInitialSetupValidationError(err):
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete initial setup"})
			}
			return
		}
		service.RecordAuditLog(service.AuditLogInput{
			LogType:    service.AuditLogTypeLogin,
			Action:     "setup_success",
			Resource:   "initial_setup",
			UserID:     uintPtr(user.ID),
			Method:     c.Request.Method,
			Path:       c.Request.URL.Path,
			StatusCode: http.StatusOK,
			IPAddress:  c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
		})
		c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
	})

	// OIDC Auth routes
	auth := r.Group("/auth")
	{
		auth.POST("/password/login",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "password-login-ip", Limit: 15, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "password-login-identity", Limit: 5, Window: time.Minute, IdentityFields: []string{"identifier"}}),
			func(c *gin.Context) {
				var input struct {
					Identifier        string `json:"identifier"`
					Password          string `json:"password"`
					CaptchaToken      string `json:"captcha_token"`
					AgreementAccepted bool   `json:"agreement_accepted"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := api.RequireAuthAgreementAccepted(input.AgreementAccepted); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				user, token, err := authService.LoginWithPassword(service.PasswordLoginInput{
					Identifier:   input.Identifier,
					Password:     input.Password,
					CaptchaToken: input.CaptchaToken,
				})
				if err != nil {
					service.RecordAuditLog(service.AuditLogInput{
						LogType:    service.AuditLogTypeLogin,
						Action:     "password_login_failed",
						Resource:   "password_login",
						Method:     c.Request.Method,
						Path:       c.Request.URL.Path,
						StatusCode: http.StatusUnauthorized,
						IPAddress:  c.ClientIP(),
						UserAgent:  c.Request.UserAgent(),
						Message:    err.Error(),
					})
					writeAuthError(c, err)
					return
				}
				service.RecordAuditLog(service.AuditLogInput{
					LogType:    service.AuditLogTypeLogin,
					Action:     "password_login_success",
					Resource:   "password_login",
					UserID:     uintPtr(user.ID),
					Method:     c.Request.Method,
					Path:       c.Request.URL.Path,
					StatusCode: http.StatusOK,
					IPAddress:  c.ClientIP(),
					UserAgent:  c.Request.UserAgent(),
				})
				c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
			})
		auth.POST("/password/register",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "password-register-ip", Limit: 5, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "password-register-email", Limit: 3, Window: time.Minute, IdentityFields: []string{"email"}}),
			func(c *gin.Context) {
				var input struct {
					Username          string `json:"username"`
					Email             string `json:"email"`
					Password          string `json:"password"`
					EmailCode         string `json:"email_code"`
					CaptchaToken      string `json:"captcha_token"`
					ReferralCode      string `json:"referral_code"`
					AgreementAccepted bool   `json:"agreement_accepted"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := api.RequireAuthAgreementAccepted(input.AgreementAccepted); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if input.ReferralCode == "" {
					input.ReferralCode = getReferralCookie(c)
				}
				user, token, err := authService.RegisterWithPassword(service.PasswordRegisterInput{
					Username:     input.Username,
					Email:        input.Email,
					Password:     input.Password,
					EmailCode:    input.EmailCode,
					CaptchaToken: input.CaptchaToken,
					ReferralCode: input.ReferralCode,
				})
				if err != nil {
					service.RecordAuditLog(service.AuditLogInput{
						LogType:    service.AuditLogTypeLogin,
						Action:     "password_register_failed",
						Resource:   "password_register",
						Method:     c.Request.Method,
						Path:       c.Request.URL.Path,
						StatusCode: http.StatusBadRequest,
						IPAddress:  c.ClientIP(),
						UserAgent:  c.Request.UserAgent(),
						Message:    err.Error(),
					})
					writeAuthError(c, err)
					return
				}
				service.RecordAuditLog(service.AuditLogInput{
					LogType:    service.AuditLogTypeLogin,
					Action:     "password_register_success",
					Resource:   "password_register",
					UserID:     uintPtr(user.ID),
					Method:     c.Request.Method,
					Path:       c.Request.URL.Path,
					StatusCode: http.StatusOK,
					IPAddress:  c.ClientIP(),
					UserAgent:  c.Request.UserAgent(),
				})
				clearReferralCookie(c)
				c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
			})
		auth.POST("/password/email-code",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "email-code-ip", Limit: 5, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "email-code-email", Limit: 3, Window: time.Minute, IdentityFields: []string{"email"}}),
			func(c *gin.Context) {
				var input struct {
					Email        string `json:"email"`
					CaptchaToken string `json:"captcha_token"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := authService.SendRegistrationEmailCode(input.Email, input.CaptchaToken); err != nil {
					writeAuthError(c, err)
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "Verification code sent"})
			})
		auth.POST("/phone/sms-code",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-code-ip", Limit: 5, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-code-phone", Limit: 2, Window: time.Minute, IdentityFields: []string{"phone"}}),
			func(c *gin.Context) {
				var input struct {
					Phone        string `json:"phone"`
					CaptchaToken string `json:"captcha_token"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := authService.SendPhoneRegistrationCode(input.Phone, input.CaptchaToken); err != nil {
					writeAuthError(c, err)
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "Verification code sent"})
			})
		auth.POST("/phone/login-code",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-login-code-ip", Limit: 5, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-login-code-phone", Limit: 2, Window: time.Minute, IdentityFields: []string{"phone"}}),
			func(c *gin.Context) {
				var input struct {
					Phone        string `json:"phone"`
					CaptchaToken string `json:"captcha_token"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := authService.SendPhoneLoginCode(input.Phone, input.CaptchaToken); err != nil {
					writeAuthError(c, err)
					return
				}
				c.JSON(http.StatusOK, gin.H{"message": "Verification code sent"})
			})
		auth.POST("/phone/login",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-login-ip", Limit: 15, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-login-phone", Limit: 5, Window: time.Minute, IdentityFields: []string{"phone"}}),
			func(c *gin.Context) {
				var input struct {
					Phone             string `json:"phone"`
					PhoneCode         string `json:"phone_code"`
					AgreementAccepted bool   `json:"agreement_accepted"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := api.RequireAuthAgreementAccepted(input.AgreementAccepted); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				user, token, err := authService.LoginWithPhoneCode(input.Phone, input.PhoneCode)
				if err != nil {
					service.RecordAuditLog(service.AuditLogInput{
						LogType:    service.AuditLogTypeLogin,
						Action:     "phone_login_failed",
						Resource:   "phone_login",
						Method:     c.Request.Method,
						Path:       c.Request.URL.Path,
						StatusCode: http.StatusUnauthorized,
						IPAddress:  c.ClientIP(),
						UserAgent:  c.Request.UserAgent(),
						Message:    err.Error(),
					})
					writeAuthError(c, err)
					return
				}
				service.RecordAuditLog(service.AuditLogInput{
					LogType:    service.AuditLogTypeLogin,
					Action:     "phone_login_success",
					Resource:   "phone_login",
					UserID:     uintPtr(user.ID),
					Method:     c.Request.Method,
					Path:       c.Request.URL.Path,
					StatusCode: http.StatusOK,
					IPAddress:  c.ClientIP(),
					UserAgent:  c.Request.UserAgent(),
				})
				c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
			})
		auth.POST("/phone/register",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-register-ip", Limit: 5, Window: time.Minute}),
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-register-phone", Limit: 3, Window: time.Minute, IdentityFields: []string{"phone"}}),
			func(c *gin.Context) {
				var input struct {
					Username          string `json:"username"`
					Phone             string `json:"phone"`
					Password          string `json:"password"`
					PhoneCode         string `json:"phone_code"`
					CaptchaToken      string `json:"captcha_token"`
					ReferralCode      string `json:"referral_code"`
					AgreementAccepted bool   `json:"agreement_accepted"`
				}
				if err := c.ShouldBindJSON(&input); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if err := api.RequireAuthAgreementAccepted(input.AgreementAccepted); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				if input.ReferralCode == "" {
					input.ReferralCode = getReferralCookie(c)
				}
				user, token, err := authService.RegisterWithPhone(service.PhoneRegisterInput{
					Username:     input.Username,
					Phone:        input.Phone,
					Password:     input.Password,
					PhoneCode:    input.PhoneCode,
					CaptchaToken: input.CaptchaToken,
					ReferralCode: input.ReferralCode,
				})
				if err != nil {
					service.RecordAuditLog(service.AuditLogInput{
						LogType:    service.AuditLogTypeLogin,
						Action:     "phone_register_failed",
						Resource:   "phone_register",
						Method:     c.Request.Method,
						Path:       c.Request.URL.Path,
						StatusCode: http.StatusBadRequest,
						IPAddress:  c.ClientIP(),
						UserAgent:  c.Request.UserAgent(),
						Message:    err.Error(),
					})
					writeAuthError(c, err)
					return
				}
				service.RecordAuditLog(service.AuditLogInput{
					LogType:    service.AuditLogTypeLogin,
					Action:     "phone_register_success",
					Resource:   "phone_register",
					UserID:     uintPtr(user.ID),
					Method:     c.Request.Method,
					Path:       c.Request.URL.Path,
					StatusCode: http.StatusOK,
					IPAddress:  c.ClientIP(),
					UserAgent:  c.Request.UserAgent(),
				})
				clearReferralCookie(c)
				c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
			})
		auth.POST("/passkey/login/options", middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "passkey-login-options-ip", Limit: 15, Window: time.Minute}), passkeyAPI.BeginLogin)
		auth.POST("/passkey/login", middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "passkey-login-ip", Limit: 15, Window: time.Minute}), passkeyAPI.CompleteLogin)
		auth.GET("/login", middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "oidc-login-ip", Limit: 20, Window: time.Minute}), func(c *gin.Context) {
			if required, err := authService.InitialSetupRequired(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load setup status"})
				return
			} else if required {
				c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is required"})
				return
			}
			if referralCode := model.NormalizeReferralCode(c.Query("ref")); referralCode != "" {
				setReferralCookie(c, referralCode, 30*24*60*60)
			}
			if err := api.RequireAuthAgreementAccepted(api.ParseAgreementAccepted(c.Query("agreement_accepted"))); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			state, err := newOIDCState()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize login"})
				return
			}

			authURL, err := authService.GetAuthURL(c.Request.Context(), state)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
				return
			}
			if authURL == "" {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OIDC is not configured"})
				return
			}

			setOIDCStateCookie(c, state, 600)
			setOIDCReturnCookie(c, authReturnURL(c), 600)
			c.Redirect(http.StatusFound, authURL)
		})
		auth.GET("/oauth/:provider/login", middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "oauth-login-ip", Limit: 20, Window: time.Minute}), func(c *gin.Context) {
			if required, err := authService.InitialSetupRequired(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load setup status"})
				return
			} else if required {
				c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is required"})
				return
			}
			if referralCode := model.NormalizeReferralCode(c.Query("ref")); referralCode != "" {
				setReferralCookie(c, referralCode, 30*24*60*60)
			}
			if err := api.RequireAuthAgreementAccepted(api.ParseAgreementAccepted(c.Query("agreement_accepted"))); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			state, err := newOIDCState()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize login"})
				return
			}

			authURL, err := authService.GetOAuthAuthURL(c.Request.Context(), c.Param("provider"), state)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
				return
			}
			if authURL == "" {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "OAuth provider is not configured"})
				return
			}

			setOIDCStateCookie(c, state, 600)
			setOIDCReturnCookie(c, authReturnURL(c), 600)
			c.Redirect(http.StatusFound, authURL)
		})
		auth.GET("/callback", func(c *gin.Context) {
			code := c.Query("code")
			state := c.Query("state")
			expectedState, err := c.Cookie(oidcStateCookie)
			returnTo := getOIDCReturnURL(c)
			clearOIDCStateCookie(c)
			clearOIDCReturnCookie(c)
			if err != nil || state == "" || state != expectedState {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OIDC state"})
				return
			}
			if code == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Missing authorization code"})
				return
			}

			referralCode := getReferralCookie(c)
			clearReferralCookie(c)
			var token string
			var loginUser *model.User
			if _, ok, err := authService.OIDCBindRequest(state); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load OIDC bind request"})
				return
			} else if ok {
				loginUser, token, err = authService.HandleOIDCBindCallback(c.Request.Context(), code, state)
			} else {
				if required, setupErr := authService.InitialSetupRequired(); setupErr != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load setup status"})
					return
				} else if required {
					c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is required"})
					return
				}
				loginUser, token, err = authService.HandleCallback(c.Request.Context(), code, referralCode)
			}
			if err != nil {
				service.RecordAuditLog(service.AuditLogInput{
					LogType:    service.AuditLogTypeLogin,
					Action:     "oidc_login_failed",
					Resource:   "oidc_callback",
					Method:     c.Request.Method,
					Path:       c.Request.URL.Path,
					StatusCode: http.StatusInternalServerError,
					IPAddress:  c.ClientIP(),
					UserAgent:  c.Request.UserAgent(),
					Message:    err.Error(),
				})
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			service.RecordAuditLog(service.AuditLogInput{
				LogType:    service.AuditLogTypeLogin,
				Action:     "oidc_login_success",
				Resource:   "oidc_callback",
				UserID:     userIDPtr(loginUser),
				Method:     c.Request.Method,
				Path:       c.Request.URL.Path,
				StatusCode: http.StatusFound,
				IPAddress:  c.ClientIP(),
				UserAgent:  c.Request.UserAgent(),
			})
			// Redirect back to frontend with token in fragment to avoid server logs.
			c.Redirect(http.StatusFound, frontendTokenRedirectURL(returnTo, token))
		})
		auth.GET("/oauth/:provider/callback", func(c *gin.Context) {
			code := c.Query("code")
			state := c.Query("state")
			expectedState, err := c.Cookie(oidcStateCookie)
			returnTo := getOIDCReturnURL(c)
			clearOIDCStateCookie(c)
			clearOIDCReturnCookie(c)
			if err != nil || state == "" || state != expectedState {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OAuth state"})
				return
			}
			if code == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Missing authorization code"})
				return
			}

			if required, setupErr := authService.InitialSetupRequired(); setupErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load setup status"})
				return
			} else if required {
				c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is required"})
				return
			}
			referralCode := getReferralCookie(c)
			clearReferralCookie(c)
			loginUser, token, err := authService.HandleOAuthCallback(c.Request.Context(), c.Param("provider"), code, referralCode)
			if err != nil {
				service.RecordAuditLog(service.AuditLogInput{
					LogType:    service.AuditLogTypeLogin,
					Action:     "oauth_login_failed",
					Resource:   "oauth_callback:" + c.Param("provider"),
					Method:     c.Request.Method,
					Path:       c.Request.URL.Path,
					StatusCode: http.StatusInternalServerError,
					IPAddress:  c.ClientIP(),
					UserAgent:  c.Request.UserAgent(),
					Message:    err.Error(),
				})
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			service.RecordAuditLog(service.AuditLogInput{
				LogType:    service.AuditLogTypeLogin,
				Action:     "oauth_login_success",
				Resource:   "oauth_callback:" + c.Param("provider"),
				UserID:     userIDPtr(loginUser),
				Method:     c.Request.Method,
				Path:       c.Request.URL.Path,
				StatusCode: http.StatusFound,
				IPAddress:  c.ClientIP(),
				UserAgent:  c.Request.UserAgent(),
			})
			c.Redirect(http.StatusFound, frontendTokenRedirectURL(returnTo, token))
		})
	}

	// AI Gateway routes (Proxy)
	rootGateway := r.Group("")
	rootGateway.Use(middleware.ExternalAPIMiddleware(), middleware.AuthMiddleware(authService), rateLimiter.Middleware())
	{
		rootGateway.GET("/balance", proxyService.HandleTokenBalance)
		rootGateway.GET("/user/balance", proxyService.HandleUserBalance)
	}

	gateway := r.Group("/v1")
	gateway.Use(middleware.ExternalAPIMiddleware(), middleware.AuthMiddleware(authService), rateLimiter.Middleware(), service.UpstreamFailureLogMiddleware())
	{
		gateway.GET("/models", proxyService.ListModels)
		gateway.GET("/balance", proxyService.HandleTokenBalance)
		gateway.GET("/user/balance", proxyService.HandleUserBalance)
		gateway.GET("/tasks/:id", proxyService.HandleUnifiedTaskStatus)
		gateway.POST("/uploads/images", proxyService.HandleUploadImage)
		gateway.POST("/audio/speech", proxyService.HandleAudioSpeech)
		gateway.POST("/audio/transcriptions", proxyService.HandleAudioTranscription)
		gateway.POST("/moderations", proxyService.HandleModeration)
		gateway.POST("/chat/completions", proxyService.HandleRequest)
		gateway.POST("/completions", proxyService.HandleRequest)
		gateway.POST("/responses", proxyService.HandleRequest)
		gateway.POST("/responses/compact", proxyService.HandleRequest)
		gateway.POST("/images/generations", proxyService.HandleImageGenerationCompatible)
		gateway.GET("/images/generations/:id", proxyService.HandleImageGenerationTaskStatus)
		gateway.POST("/images/edits", proxyService.HandleImageEdit)
		gateway.POST("/videos/generations", proxyService.HandleVideoGenerationCompatible)
		gateway.GET("/videos/generations/:id", proxyService.HandleUnifiedTaskStatus)
		gateway.POST("/videos/:id/remix", proxyService.HandleVideoRemix)
		gateway.POST("/seedance2/private-avatar", proxyService.HandleSeedancePrivateAvatar)
		gateway.POST("/video/generations", proxyService.HandleVideoTaskCreate)
		gateway.GET("/video/generations/:id", proxyService.HandleVideoTaskStatus)
		gateway.POST("/videos/image2video", proxyService.HandleVideoTaskCreate)
		gateway.GET("/videos/image2video/:id", proxyService.HandleVideoTaskStatus)
		gateway.POST("/video/tasks", proxyService.HandleVideoTaskCreate)
		gateway.GET("/video/tasks/:id", proxyService.HandleVideoTaskStatus)
		gateway.POST("/videos/tasks", proxyService.HandleVideoTaskCreate)
		gateway.GET("/videos/tasks/:id", proxyService.HandleVideoTaskStatus)
		gateway.POST("/midjourney/generations", proxyService.HandleMidjourneyCreate)
		gateway.POST("/midjourney/generations/imagine", proxyService.HandleMidjourneyCreate)
		for _, action := range []string{"blend", "describe", "edits", "high-variation", "inpaint", "low-variation", "modal", "pan", "reroll", "upscale", "variation", "video", "zoom", "remix"} {
			gateway.POST("/midjourney/generations/"+action, proxyService.HandleMidjourneyCreate)
		}
		gateway.GET("/midjourney/:id", proxyService.HandleMidjourneyStatus)
		gateway.POST("/messages", proxyService.HandleClaudeMessages)
		gateway.POST("/models/:modelAction", proxyService.HandleGeminiGenerateContent)
	}

	geminiGateway := r.Group("/v1beta")
	geminiGateway.Use(middleware.ExternalAPIMiddleware(), middleware.AuthMiddleware(authService), rateLimiter.Middleware(), service.UpstreamFailureLogMiddleware())
	{
		geminiGateway.POST("/models/:modelAction", proxyService.HandleGeminiGenerateContent)
	}

	// Admin/Dashboard APIs
	admin := r.Group("/api")
	admin.Use(middleware.AuthMiddleware(authService), middleware.AdminMiddleware())
	{
		// System settings
		admin.GET("/settings", systemAPI.GetSettings)
		admin.PUT("/settings", systemAPI.UpdateSettings)
		admin.POST("/settings/export", systemAPI.ExportConfiguration)
		admin.POST("/settings/import", systemAPI.ImportConfiguration)
		admin.DELETE("/logs", systemAPI.DeleteLogs)
		admin.GET("/nodes", clusterAPI.Status)
		admin.POST("/nodes/:id/promote", clusterAPI.PromoteNode)
		admin.DELETE("/nodes/:id", clusterAPI.DeleteNode)
		admin.GET("/updates", systemAPI.GetAutoUpdateStatus)
		admin.POST("/updates/check", systemAPI.CheckForUpdate)
		admin.POST("/updates/apply", systemAPI.StartAutoUpdate)
		admin.POST("/restart", systemAPI.Restart)
		admin.GET("/status-monitors", statusMonitorAPI.List)
		admin.POST("/status-monitors", statusMonitorAPI.Create)
		admin.PUT("/status-monitors/:id", statusMonitorAPI.Update)
		admin.DELETE("/status-monitors/:id", statusMonitorAPI.Delete)
		admin.POST("/status-monitors/:id/check", statusMonitorAPI.CheckNow)
		admin.GET("/announcements", announcementAPI.List)
		admin.POST("/announcements", announcementAPI.Create)
		admin.PUT("/announcements/:id", announcementAPI.Update)
		admin.DELETE("/announcements/:id", announcementAPI.Delete)

		// Channels
		admin.GET("/channels", channelAPI.List)
		admin.POST("/channels", channelAPI.Create)
		admin.PUT("/channels/:id", channelAPI.Update)
		admin.PUT("/channels/:id/group-multipliers", channelAPI.SetGroupMultipliers)
		admin.GET("/channels/:id/models", modelAPI.ListChannelModels)
		admin.POST("/channels/:id/models", modelAPI.CreateChannelModel)
		admin.DELETE("/channels/:id", channelAPI.Delete)
		admin.POST("/channels/sync", channelAPI.Sync)

		// Global models and upstream channel model configs
		admin.GET("/models", modelAPI.List)
		admin.POST("/models", modelAPI.Create)
		admin.POST("/models/sync", modelAPI.Sync)
		admin.POST("/models/sync/preview", modelAPI.PreviewSync)
		admin.POST("/models/sync/preview/browser", modelAPI.PreviewSyncFromBrowser)
		admin.POST("/models/sync/apply", modelAPI.ApplySync)
		admin.POST("/models/prices/sync/preview", modelAPI.PreviewPriceSync)
		admin.POST("/models/prices/sync/preview/browser", modelAPI.PreviewPriceSyncFromBrowser)
		admin.POST("/models/prices/sync/apply", modelAPI.ApplyPriceSync)
		admin.POST("/models/prices/sync/preview/community", modelAPI.PreviewCommunityPriceSync)
		admin.POST("/models/prices/sync/apply/community", modelAPI.ApplyCommunityPriceSync)
		admin.PUT("/models/:id", modelAPI.Update)
		admin.DELETE("/models/:id", modelAPI.Delete)
		admin.PUT("/channel-models/:id", modelAPI.UpdateChannelModel)
		admin.PUT("/channel-models/:id/group-multipliers", modelAPI.SetGroupMultipliers)
		admin.DELETE("/channel-models/:id", modelAPI.DeleteChannelModel)

		// User-facing channels
		admin.GET("/user-channels", userChannelAPI.List)
		admin.POST("/user-channels", userChannelAPI.Create)
		admin.PUT("/user-channels/:id", userChannelAPI.Update)
		admin.PUT("/user-channels/:id/allowed-groups", userChannelAPI.SetAllowedGroups)
		admin.PUT("/user-channels/:id/allowed-users", userChannelAPI.SetAllowedUsers)
		admin.DELETE("/user-channels/:id", userChannelAPI.Delete)

		// Groups
		admin.GET("/groups", groupAPI.List)
		admin.POST("/groups", groupAPI.Create)
		admin.PUT("/groups/:id", groupAPI.Update)
		admin.DELETE("/groups/:id", groupAPI.Delete)

		// Users
		admin.GET("/users", userAPI.List)
		admin.PUT("/users/:id", userAPI.Update)
		admin.DELETE("/users/:id", userAPI.Delete)
		admin.GET("/referral-commissions", referralAPI.ListCommissions)

		// Stats
		admin.GET("/logs", statsAPI.GetLogs)
		admin.GET("/audit-logs", statsAPI.GetAuditLogs)
		admin.GET("/stats", statsAPI.GetDashboardStats)
		admin.GET("/channel-usage", statsAPI.GetChannelUsage)
		service.RegisterCommunityAdvancedChatAdminRoutes(admin)
		service.RegisterCloudSandboxAdminRoutes(admin)
		messagechannel.RegisterAdminRoutes(admin)
		service.ApplyAdminRouteHooks(admin)
	}

	adminTickets := r.Group("/api/admin")
	adminTickets.Use(middleware.AuthMiddleware(authService), middleware.AdminMiddleware())
	service.RegisterTicketAdminRoutes(adminTickets)

	// User Self APIs
	publicAPI := r.Group("/api")
	service.RegisterCommunityAdvancedChatPublicRoutes(publicAPI)
	messagechannel.RegisterPublicRoutes(publicAPI)
	service.ApplyPublicAPIRouteHooks(publicAPI)

	userGroup := r.Group("/api/user")
	userGroup.Use(middleware.AuthMiddleware(authService), middleware.TenantContextMiddleware())
	{
		userGroup.GET("/me", userAPI.GetMe)
		userGroup.POST("/avatar", userAPI.UploadAvatar)
		userGroup.GET("/catalog", userChannelAPI.Catalog)
		userGroup.GET("/stats", statsAPI.GetUserDashboardStats)
		userGroup.GET("/logs", statsAPI.GetUserLogs)
		userGroup.GET("/referral", referralAPI.GetMine)
		userGroup.GET("/check-in/status", checkInAPI.Status)
		userGroup.GET("/check-in/records", checkInAPI.ListRecords)
		userGroup.POST("/check-in", checkInAPI.Claim)
		userGroup.GET("/payment/config", paymentAPI.Config)
		userGroup.GET("/payment/orders", paymentAPI.ListOrders)
		userGroup.POST("/payment/orders", paymentAPI.CreateOrder)
		userGroup.GET("/payment/orders/:order_no", paymentAPI.GetOrder)
		userGroup.GET("/passkeys", passkeyAPI.List)
		userGroup.POST("/passkeys/register/options", passkeyAPI.BeginRegistration)
		userGroup.POST("/passkeys/register", passkeyAPI.CompleteRegistration)
		userGroup.DELETE("/passkeys/:id", passkeyAPI.Delete)
		userGroup.GET("/password/method", userAPI.PasswordChangeMethod)
		userGroup.POST("/password/email-code", userAPI.SendPasswordChangeEmailCode)
		userGroup.POST("/password/change", userAPI.ChangePassword)
		userGroup.POST("/phone/bind-code",
			middleware.NewSensitiveRateLimiter(middleware.SensitiveRateLimitConfig{Name: "phone-bind-code-ip", Limit: 5, Window: time.Minute}),
			userAPI.SendPhoneBindCode)
		userGroup.POST("/phone/bind", userAPI.BindPhone)
		userGroup.POST("/oidc/bind-url", func(c *gin.Context) {
			val, exists := c.Get("user")
			if !exists {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
				return
			}
			user, ok := val.(*model.User)
			if !ok || user == nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
				return
			}
			state, err := newOIDCState()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize OIDC binding"})
				return
			}
			authURL, err := authService.CreateOIDCBindRequest(c.Request.Context(), user.ID, state)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			setOIDCStateCookie(c, state, 600)
			c.JSON(http.StatusOK, gin.H{"auth_url": authURL})
		})
		userGroup.GET("/api-keys", userAPI.ListAPIKeys)
		userGroup.POST("/api-keys", userAPI.CreateAPIKey)
		userGroup.PUT("/api-keys/:id", userAPI.UpdateAPIKey)
		userGroup.POST("/api-keys/:id/rotate", userAPI.RotateAPIKey)
		userGroup.POST("/api-keys/:id/reset-usage", userAPI.ResetAPIKeyUsage)
		userGroup.DELETE("/api-keys/:id", userAPI.DeleteAPIKey)
		userGroup.POST("/api-key/rotate", userAPI.RotateAPIKey)
		service.RegisterTicketUserRoutes(userGroup)
		userGroup.GET("/enterprise/organization", enterpriseAPI.GetOrganization)
		userGroup.PUT("/enterprise/organization", middleware.PermissionMiddleware("organization.manage"), enterpriseAPI.UpdateOrganization)
		userGroup.GET("/enterprise/portal", enterpriseAPI.GetPortal)
		userGroup.PUT("/enterprise/portal", middleware.PermissionMiddleware("organization.manage"), enterpriseAPI.UpdatePortal)
		userGroup.PUT("/enterprise/portal/layout", middleware.PermissionMiddleware("organization.manage"), enterpriseAPI.UpdatePortalLayout)
		userGroup.GET("/enterprise/workspaces", enterpriseAPI.ListWorkspaces)
		userGroup.POST("/enterprise/context", enterpriseAPI.SelectContext)
		userGroup.GET("/enterprise/permissions/preview", enterpriseAPI.PreviewPermissions)
		userGroup.GET("/enterprise/permissions", middleware.PermissionMiddleware("role.read"), enterpriseAPI.ListPermissions)
		userGroup.GET("/enterprise/members", middleware.PermissionMiddleware("member.read"), enterpriseAPI.ListMembers)
		userGroup.POST("/enterprise/members", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.CreateMember)
		userGroup.PATCH("/enterprise/members/:user_id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.UpdateMember)
		userGroup.DELETE("/enterprise/members/:user_id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.DeleteMember)
		userGroup.GET("/enterprise/members/:user_id/departments", middleware.PermissionMiddleware("member.read"), enterpriseAPI.ListMemberDepartments)
		userGroup.PUT("/enterprise/members/:user_id/departments", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.ReplaceMemberDepartments)
		userGroup.GET("/enterprise/departments", middleware.PermissionMiddleware("member.read"), enterpriseAPI.ListDepartments)
		userGroup.POST("/enterprise/departments", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.CreateDepartment)
		userGroup.PUT("/enterprise/departments/:id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.UpdateDepartment)
		userGroup.DELETE("/enterprise/departments/:id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.DeleteDepartment)
		userGroup.GET("/enterprise/departments/:id/roles", middleware.PermissionMiddleware("role.read"), enterpriseAPI.ListDepartmentRoles)
		userGroup.PUT("/enterprise/departments/:id/roles", middleware.PermissionMiddleware("role.manage"), enterpriseAPI.ReplaceDepartmentRoles)
		userGroup.GET("/enterprise/roles", middleware.PermissionMiddleware("role.read"), enterpriseAPI.ListRoles)
		userGroup.POST("/enterprise/roles", middleware.PermissionMiddleware("role.manage"), enterpriseAPI.CreateRole)
		userGroup.PUT("/enterprise/roles/:id", middleware.PermissionMiddleware("role.manage"), enterpriseAPI.UpdateRole)
		userGroup.GET("/enterprise/role-bindings", middleware.PermissionMiddleware("role.read"), enterpriseAPI.ListRoleBindings)
		userGroup.POST("/enterprise/role-bindings", middleware.PermissionMiddleware("role.manage"), enterpriseAPI.CreateRoleBinding)
		userGroup.DELETE("/enterprise/role-bindings/:id", middleware.PermissionMiddleware("role.manage"), enterpriseAPI.DeleteRoleBinding)
		userGroup.GET("/enterprise/tasks", enterpriseAPI.ListTasks)
		userGroup.POST("/enterprise/tasks", enterpriseAPI.CreateTask)
		userGroup.PATCH("/enterprise/tasks/:id/status", enterpriseAPI.UpdateTaskStatus)
		userGroup.GET("/enterprise/tasks/:id", enterpriseAPI.GetTaskDetail)
		userGroup.GET("/enterprise/managed-tasks", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.ListManagedTasks)
		userGroup.GET("/enterprise/managed-tasks/:id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.GetManagedTaskDetail)
		userGroup.POST("/enterprise/managed-tasks/:id/participants", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.AddTaskParticipant)
		userGroup.DELETE("/enterprise/managed-tasks/:id/participants/:user_id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.DeleteTaskParticipant)
		userGroup.POST("/enterprise/managed-tasks/:id/departments", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.AddTaskDepartment)
		userGroup.DELETE("/enterprise/managed-tasks/:id/departments/:department_id", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.DeleteTaskDepartment)
		userGroup.GET("/enterprise/shared-pools", enterpriseAPI.ListSharedPools)
		userGroup.POST("/enterprise/shared-pools", middleware.PermissionMiddleware("member.manage"), enterpriseAPI.CreateSharedPool)
		userGroup.GET("/enterprise/shared-pools/:id/sessions", enterpriseAPI.ListSharedPoolSessions)
		userGroup.POST("/enterprise/shared-pools/:id/sessions/new", enterpriseAPI.CreateSharedPoolSession)
		userGroup.GET("/enterprise/shared-pools/:id/devices", enterpriseAPI.ListSharedPoolDevices)
		userGroup.GET("/enterprise/shared-pools/:id/sessions/:session_id", enterpriseAPI.GetSharedPoolSession)
		userGroup.POST("/enterprise/shared-pools/:id/sessions/:session_id/messages", enterpriseAPI.AppendSharedPoolSessionMessage)
		userGroup.POST("/enterprise/shared-pools/:id/sessions", enterpriseAPI.ShareSessionToPool)
		userGroup.GET("/enterprise/shared-pools/:id/files", enterpriseAPI.ListSharedPoolFiles)
		userGroup.GET("/enterprise/shared-pools/:id/files/:file_id/content", enterpriseAPI.GetSharedPoolFileContent)
		userGroup.GET("/enterprise/shared-pools/:id/files/:file_id/download", enterpriseAPI.DownloadSharedPoolFile)
		userGroup.POST("/enterprise/shared-pools/:id/files", enterpriseAPI.ShareFileToPool)
		userGroup.GET("/enterprise/devices", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.ListDevices)
		userGroup.POST("/enterprise/devices/connector-command", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.CreateConnectorCommand)
		userGroup.POST("/enterprise/devices/:id/connector-command", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.RotateConnectorCommand)
		userGroup.POST("/enterprise/devices", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.CreateDevice)
		userGroup.PUT("/enterprise/devices/:id", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.UpdateDevice)
		userGroup.DELETE("/enterprise/devices/:id", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.DeleteDevice)
		userGroup.GET("/enterprise/device-assignments", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.ListDeviceAssignments)
		userGroup.POST("/enterprise/device-assignments", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.AssignDevice)
		userGroup.POST("/enterprise/device-assignments/:id/revoke", middleware.PermissionMiddleware("tool.manage"), enterpriseAPI.RevokeDeviceAssignment)
		userGroup.GET("/enterprise/quota-accounts", middleware.PermissionMiddleware("cost.read"), enterpriseAPI.ListQuotaAccounts)
		userGroup.POST("/enterprise/quota-accounts", middleware.PermissionMiddleware("cost.manage"), enterpriseAPI.CreateQuotaAccount)
		userGroup.POST("/enterprise/quota-allocations", middleware.PermissionMiddleware("cost.pool_fund"), enterpriseAPI.AllocateQuota)
		userGroup.POST("/enterprise/pools/personal-budget", enterpriseAPI.FundPoolFromPersonalBalance)
		userGroup.POST("/enterprise/pools/organization-budget", middleware.PermissionMiddleware("cost.pool_fund"), enterpriseAPI.FundPoolFromOrganizationBudget)
		userGroup.POST("/enterprise/pools/organization-budget/reclaim", middleware.PermissionMiddleware("cost.pool_reclaim"), enterpriseAPI.ReclaimPoolToOrganizationBudget)
		userGroup.POST("/enterprise/user-budgets", middleware.PermissionMiddleware("cost.user_grant"), enterpriseAPI.GrantOrganizationBudgetToUser)
		userGroup.GET("/enterprise/quota-ledger", middleware.PermissionMiddleware("cost.read"), enterpriseAPI.ListQuotaLedger)
		service.RegisterPersonalCompanyUserRoutes(userGroup)
		service.RegisterCommunityAdvancedChatUserRoutes(userGroup)
		service.RegisterCloudSandboxUserRoutes(userGroup)
		messagechannel.RegisterUserRoutes(userGroup)
		service.ApplyUserRouteHooks(userGroup)
	}

	// Serve embedded frontend assets from web/dist.
	assetsFS, err := fs.Sub(frontendFS, "assets")
	if err != nil {
		return err
	}
	r.StaticFS("/assets", http.FS(assetsFS))
	r.GET("/favicon.svg", embeddedFileHandler(frontendFS, "favicon.svg"))
	r.GET("/icons.svg", embeddedFileHandler(frontendFS, "icons.svg"))
	r.NoRoute(func(c *gin.Context) {
		// If the request starts with an API/auth prefix, it's a 404
		path := c.Request.URL.Path
		if hasRoutePrefix(path, "/api") || hasRoutePrefix(path, "/v1") || hasRoutePrefix(path, "/v1beta") || hasRoutePrefix(path, "/auth") {
			c.JSON(404, gin.H{"error": "Not found"})
			return
		}
		// Otherwise, serve index.html for SPA routing
		serveEmbeddedFile(c, frontendFS, "index.html")
	})

	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	restartDone := make(chan error, 1)
	var restartState struct {
		sync.Mutex
		started bool
	}
	scheduleRestart := func(restart func() error) error {
		restartState.Lock()
		if restartState.started {
			restartState.Unlock()
			return errors.New("service restart is already in progress")
		}
		restartState.started = true
		restartState.Unlock()
		go func() {
			// Let the accepted response reach the administrator before draining.
			time.Sleep(200 * time.Millisecond)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				restartDone <- fmt.Errorf("stop server for restart: %w", err)
				return
			}
			restartDone <- restart()
		}()
		return nil
	}
	service.RegisterApplicationRestart(func() error {
		return scheduleRestart(service.RestartCurrentProcess)
	})
	defer service.RegisterApplicationRestart(nil)
	updater := service.NewAutoUpdateService()
	service.RegisterAutoUpdateService(updater)
	updater.Start(func(stagedBinary string) error {
		return scheduleRestart(func() error { return service.RestartWithStagedUpdate(stagedBinary) })
	})

	err = server.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	select {
	case restartErr := <-restartDone:
		return restartErr
	case <-time.After(35 * time.Second):
		return errors.New("timed out while applying automatic update")
	}
}

// MigrateSQLiteDatabase runs the explicit one-way --migrate command. Normal
// startup continues to use DB_DRIVER and never invokes this operation.
func MigrateSQLiteDatabase() error {
	config.Init()
	if config.DBDriver != "postgres" && config.DBDriver != "postgresql" && config.DBDriver != "mysql" && config.DBDriver != "mariadb" {
		return errors.New("--migrate requires DB_DRIVER=postgres or DB_DRIVER=mysql for the target database")
	}
	report, err := model.MigrateSQLiteToTarget(config.DBPath, config.DBDriver, config.DBDSN)
	if err != nil {
		return err
	}
	log.Printf("SQLite migration completed: %d rows across %d tables copied to %s (%d unusable records discarded, %d dangling references repaired)", report.Rows, report.Tables, report.TargetDriver, report.DiscardedRows, report.RepairedRows)
	return nil
}

const oidcStateCookie = "flai_oidc_state"
const oidcReturnCookie = "flai_oidc_return_to"
const referralCookie = "flai_referral_code"
const maxRequestBodyBytes int64 = 128 << 20

func requestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func writeAuthError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrInitialSetupRequired) {
		c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is required"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

func embeddedFileHandler(fileSystem fs.FS, name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		serveEmbeddedFile(c, fileSystem, name)
	}
}

func serveEmbeddedFile(c *gin.Context, fileSystem fs.FS, name string) {
	data, err := fs.ReadFile(fileSystem, name)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Data(http.StatusOK, contentType, data)
}

func newOIDCState() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func setOIDCStateCookie(c *gin.Context, state string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    state,
		Path:     "/auth",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Request.TLS != nil,
	})
}

func clearOIDCStateCookie(c *gin.Context) {
	setOIDCStateCookie(c, "", -1)
}

func setOIDCReturnCookie(c *gin.Context, returnTo string, maxAge int) {
	if returnTo != "" {
		returnTo = base64.RawURLEncoding.EncodeToString([]byte(returnTo))
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     oidcReturnCookie,
		Value:    returnTo,
		Path:     "/auth",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Request.TLS != nil,
	})
}

func getOIDCReturnURL(c *gin.Context) string {
	value, err := c.Cookie(oidcReturnCookie)
	if err != nil || value == "" {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return ""
	}
	return string(decoded)
}

func clearOIDCReturnCookie(c *gin.Context) {
	setOIDCReturnCookie(c, "", -1)
}

func setReferralCookie(c *gin.Context, code string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     referralCookie,
		Value:    code,
		Path:     "/auth",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   c.Request.TLS != nil,
	})
}

func getReferralCookie(c *gin.Context) string {
	value, err := c.Cookie(referralCookie)
	if err != nil {
		return ""
	}
	return model.NormalizeReferralCode(value)
}

func clearReferralCookie(c *gin.Context) {
	setReferralCookie(c, "", -1)
}

func authReturnURL(c *gin.Context) string {
	if !config.IsDevelopmentLike(config.Environment) {
		return ""
	}
	if origin := originFromURL(c.GetHeader("Referer")); origin != "" {
		return origin
	}
	return originFromURL(c.GetHeader("Origin"))
}

func frontendTokenRedirectURL(returnTo, token string) string {
	escapedToken := url.QueryEscape(token)
	base := strings.TrimSpace(returnTo)
	if base == "" {
		return "/dashboard?token=" + escapedToken
	}

	parsed, err := url.Parse(base)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + "/dashboard"
		parsed.RawQuery = "token=" + escapedToken
		parsed.Fragment = ""
		return parsed.String()
	}

	return strings.TrimRight(base, "/") + "/dashboard?token=" + escapedToken
}

func originFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func hasRoutePrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func uintPtr(value uint) *uint {
	return &value
}

func userIDPtr(user *model.User) *uint {
	if user == nil || user.ID == 0 {
		return nil
	}
	return uintPtr(user.ID)
}
