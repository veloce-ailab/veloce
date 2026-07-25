package service

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type ticketAPI struct{}

type ticketInput struct {
	Subject      string `json:"subject"`
	Category     string `json:"category"`
	Priority     string `json:"priority"`
	Content      string `json:"content"`
	CaptchaToken string `json:"captcha_token"`
}

type ticketMessageInput struct {
	Content string `json:"content"`
}

type ticketStatusInput struct {
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

type ticketUserResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type ticketMessageResponse struct {
	ID        uint               `json:"id"`
	TicketID  uint               `json:"ticket_id"`
	UserID    uint               `json:"user_id"`
	IsStaff   bool               `json:"is_staff"`
	Content   string             `json:"content"`
	CreatedAt time.Time          `json:"created_at"`
	Author    ticketUserResponse `json:"author"`
}

type ticketResponse struct {
	ID        uint                    `json:"id"`
	UserID    uint                    `json:"user_id"`
	Subject   string                  `json:"subject"`
	Category  string                  `json:"category"`
	Priority  string                  `json:"priority"`
	Status    string                  `json:"status"`
	ClosedAt  *time.Time              `json:"closed_at,omitempty"`
	CreatedAt time.Time               `json:"created_at"`
	UpdatedAt time.Time               `json:"updated_at"`
	Requester ticketUserResponse      `json:"requester"`
	Messages  []ticketMessageResponse `json:"messages,omitempty"`
}

// RegisterTicketAdminRoutes exposes the support queue to site administrators.
func RegisterTicketAdminRoutes(group *gin.RouterGroup) {
	api := &ticketAPI{}
	group.GET("/tickets", api.listAll)
	group.GET("/tickets/:id", api.getAdmin)
	group.PATCH("/tickets/:id", api.update)
	group.POST("/tickets/:id/messages", api.addAdminMessage)
}

// RegisterTicketUserRoutes exposes a requester's own support tickets.
func RegisterTicketUserRoutes(group *gin.RouterGroup) {
	api := &ticketAPI{}
	group.GET("/tickets", api.listMine)
	group.POST("/tickets", api.create)
	group.GET("/tickets/:id", api.getMine)
	group.POST("/tickets/:id/messages", api.addUserMessage)
	group.POST("/tickets/:id/close", api.close)
}

func (api *ticketAPI) listMine(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok {
		return
	}
	var tickets []model.Ticket
	if err := model.DB.Where("user_id = ?", user.ID).Order("updated_at DESC").Find(&tickets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load tickets"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tickets": ticketResponses(tickets, nil, false)})
}

func (api *ticketAPI) listAll(c *gin.Context) {
	if _, ok := currentTicketUser(c); !ok {
		return
	}
	var tickets []model.Ticket
	if err := model.DB.Order("updated_at DESC").Find(&tickets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load tickets"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tickets": ticketResponses(tickets, nil, false)})
}

func (api *ticketAPI) create(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok {
		return
	}
	var input ticketInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ticket"})
		return
	}
	subject := strings.TrimSpace(input.Subject)
	content := strings.TrimSpace(input.Content)
	if subject == "" || len([]rune(subject)) > 160 || content == "" || len([]rune(content)) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subject must be 1-160 characters and content must be 1-5000 characters"})
		return
	}
	category := normalizedTicketCategory(input.Category)
	priority := normalizedTicketPriority(input.Priority)
	if err := verifyTicketHCaptcha(input.CaptchaToken); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ticket := model.Ticket{UserID: user.ID, Subject: subject, Category: category, Priority: priority, Status: model.TicketStatusOpen}
	message := model.TicketMessage{UserID: user.ID, IsStaff: false, Content: content}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ticket).Error; err != nil {
			return err
		}
		message.TicketID = ticket.ID
		return tx.Create(&message).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create ticket"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"ticket": ticketResponseFromModel(ticket, user, false)})
}

func (api *ticketAPI) getMine(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok {
		return
	}
	ticket, ok := ticketFromRequest(c, true)
	if !ok {
		return
	}
	if ticket.UserID != user.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ticket not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticketResponseFromModel(ticket, nil, true)})
}

func (api *ticketAPI) getAdmin(c *gin.Context) {
	if _, ok := currentTicketUser(c); !ok {
		return
	}
	ticket, ok := ticketFromRequest(c, true)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticketResponseFromModel(ticket, nil, true)})
}

func (api *ticketAPI) addUserMessage(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok {
		return
	}
	ticket, ok := ticketFromRequest(c, false)
	if !ok {
		return
	}
	if ticket.UserID != user.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ticket not found"})
		return
	}
	api.addMessage(c, ticket, user, false)
}

func (api *ticketAPI) addAdminMessage(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok {
		return
	}
	ticket, ok := ticketFromRequest(c, false)
	if !ok {
		return
	}
	api.addMessage(c, ticket, user, true)
}

func (api *ticketAPI) addMessage(c *gin.Context, ticket model.Ticket, user *model.User, isStaff bool) {
	if ticket.Status == model.TicketStatusClosed {
		c.JSON(http.StatusConflict, gin.H{"error": "Closed tickets cannot receive new messages"})
		return
	}
	var input ticketMessageInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid message"})
		return
	}
	content := strings.TrimSpace(input.Content)
	if content == "" || len([]rune(content)) > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message must be between 1 and 5000 characters"})
		return
	}
	message := model.TicketMessage{TicketID: ticket.ID, UserID: user.ID, IsStaff: isStaff, Content: content}
	status := model.TicketStatusOpen
	if isStaff {
		status = model.TicketStatusAnswered
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		return tx.Model(&ticket).Updates(map[string]interface{}{"status": status, "closed_at": nil}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add message"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": ticketMessageResponseFromModel(message, user)})
}

func (api *ticketAPI) update(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok || !user.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Administrator access is required"})
		return
	}
	ticket, ok := ticketFromRequest(c, false)
	if !ok {
		return
	}
	var input ticketStatusInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ticket update"})
		return
	}
	updates := map[string]interface{}{}
	if strings.TrimSpace(input.Status) != "" {
		status, valid := normalizedTicketStatus(input.Status)
		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ticket status"})
			return
		}
		updates["status"] = status
		if status == model.TicketStatusClosed {
			now := time.Now()
			updates["closed_at"] = &now
		} else {
			updates["closed_at"] = nil
		}
	}
	if strings.TrimSpace(input.Priority) != "" {
		updates["priority"] = normalizedTicketPriority(input.Priority)
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No ticket fields to update"})
		return
	}
	if err := model.DB.Model(&ticket).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update ticket"})
		return
	}
	if err := model.DB.First(&ticket, ticket.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load ticket"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": ticketResponseFromModel(ticket, nil, false)})
}

func (api *ticketAPI) close(c *gin.Context) {
	user, ok := currentTicketUser(c)
	if !ok {
		return
	}
	ticket, ok := ticketFromRequest(c, false)
	if !ok {
		return
	}
	if ticket.UserID != user.ID {
		c.JSON(http.StatusNotFound, gin.H{"error": "Ticket not found"})
		return
	}
	now := time.Now()
	if err := model.DB.Model(&ticket).Updates(map[string]interface{}{"status": model.TicketStatusClosed, "closed_at": &now}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to close ticket"})
		return
	}
	c.Status(http.StatusNoContent)
}

func currentTicketUser(c *gin.Context) (*model.User, bool) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
	}
	return user, ok
}

func ticketFromRequest(c *gin.Context, withMessages bool) (model.Ticket, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ticket ID"})
		return model.Ticket{}, false
	}
	query := model.DB
	if withMessages {
		query = query.Preload("Messages", func(db *gorm.DB) *gorm.DB { return db.Order("created_at ASC") })
	}
	var ticket model.Ticket
	if err := query.First(&ticket, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "Ticket not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load ticket"})
		}
		return model.Ticket{}, false
	}
	return ticket, true
}

func verifyTicketHCaptcha(token string) error {
	if !settingBool("password_hcaptcha_enabled", false) ||
		strings.TrimSpace(model.GetSystemSetting("hcaptcha_site_key", "")) == "" ||
		strings.TrimSpace(model.GetSystemSetting("hcaptcha_secret", "")) == "" {
		return nil
	}
	return verifyHCaptcha(token)
}

func ticketResponses(tickets []model.Ticket, users map[uint]*model.User, includeMessages bool) []ticketResponse {
	if users == nil {
		users = ticketUsers(tickets)
	}
	responses := make([]ticketResponse, 0, len(tickets))
	for _, ticket := range tickets {
		responses = append(responses, ticketResponseFromModel(ticket, users[ticket.UserID], includeMessages))
	}
	return responses
}

func ticketUsers(tickets []model.Ticket) map[uint]*model.User {
	ids := make([]uint, 0, len(tickets))
	for _, ticket := range tickets {
		ids = append(ids, ticket.UserID)
		for _, message := range ticket.Messages {
			ids = append(ids, message.UserID)
		}
	}
	users := map[uint]*model.User{}
	if len(ids) == 0 {
		return users
	}
	var records []model.User
	if model.DB.Where("id IN ?", ids).Find(&records).Error == nil {
		for index := range records {
			users[records[index].ID] = &records[index]
		}
	}
	return users
}

func ticketResponseFromModel(ticket model.Ticket, requester *model.User, includeMessages bool) ticketResponse {
	if requester == nil {
		users := ticketUsers([]model.Ticket{ticket})
		requester = users[ticket.UserID]
	}
	response := ticketResponse{ID: ticket.ID, UserID: ticket.UserID, Subject: ticket.Subject, Category: ticket.Category, Priority: ticket.Priority, Status: ticket.Status, ClosedAt: ticket.ClosedAt, CreatedAt: ticket.CreatedAt, UpdatedAt: ticket.UpdatedAt, Requester: ticketUserResponseFromModel(requester)}
	if includeMessages {
		users := ticketUsers([]model.Ticket{ticket})
		response.Messages = make([]ticketMessageResponse, 0, len(ticket.Messages))
		for _, message := range ticket.Messages {
			response.Messages = append(response.Messages, ticketMessageResponseFromModel(message, users[message.UserID]))
		}
	}
	return response
}

func ticketMessageResponseFromModel(message model.TicketMessage, author *model.User) ticketMessageResponse {
	return ticketMessageResponse{ID: message.ID, TicketID: message.TicketID, UserID: message.UserID, IsStaff: message.IsStaff, Content: message.Content, CreatedAt: message.CreatedAt, Author: ticketUserResponseFromModel(author)}
}

func ticketUserResponseFromModel(user *model.User) ticketUserResponse {
	if user == nil {
		return ticketUserResponse{}
	}
	return ticketUserResponse{ID: user.ID, Username: user.Username, Email: user.Email}
}

func normalizedTicketCategory(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "billing" || value == "account" || value == "technical" || value == "suggestion" {
		return value
	}
	return "general"
}

func normalizedTicketPriority(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "low" || value == "high" || value == "urgent" {
		return value
	}
	return "normal"
}

func normalizedTicketStatus(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case model.TicketStatusOpen, model.TicketStatusPending, model.TicketStatusAnswered, model.TicketStatusClosed:
		return value, true
	default:
		return "", false
	}
}
