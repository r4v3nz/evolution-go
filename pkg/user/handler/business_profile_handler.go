// Fork change (r4v3nz/evolution-go): this file is not part of upstream Evolution Go.

package user_handler

import (
	"errors"
	"net/http"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	user_service "github.com/evolution-foundation/evolution-go/pkg/user/service"
	"github.com/gin-gonic/gin"
)

// Get the instance's WhatsApp Business profile
// @Summary Get the WhatsApp Business profile
// @Description Reads description, address, e-mail, websites, categories and business hours of the instance's own account. isBusiness is false for a regular WhatsApp account.
// @Tags User
// @Produce json
// @Success 200 {object} gin.H "success"
// @Failure 500 {object} gin.H "Internal server error"
// @Router /user/businessProfile [get]
func (u *userHandler) GetBusinessProfile(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	profile, err := u.userService.GetBusinessProfile(instance)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": profile})
}

// Edit the instance's WhatsApp Business profile
// @Summary Edit the WhatsApp Business profile
// @Description Changes only the fields sent (description, address, email, websites, hours); an empty value clears the field. Fails with 422 for a regular WhatsApp account.
// @Tags User
// @Accept json
// @Produce json
// @Param message body user_service.SetBusinessProfileStruct true "Business profile fields"
// @Success 200 {object} gin.H "success"
// @Failure 400 {object} gin.H "Invalid field"
// @Failure 422 {object} gin.H "Not a WhatsApp Business account"
// @Failure 500 {object} gin.H "Internal server error"
// @Router /user/businessProfile [post]
func (u *userHandler) SetBusinessProfile(ctx *gin.Context) {
	instance, ok := ctx.MustGet("instance").(*instance_model.Instance)
	if !ok {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "instance not found"})
		return
	}

	var data user_service.SetBusinessProfileStruct
	if err := ctx.ShouldBindBodyWithJSON(&data); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	profile, err := u.userService.SetBusinessProfile(&data, instance)
	switch {
	case errors.Is(err, user_service.ErrInvalidBusinessProfile):
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	case errors.Is(err, user_service.ErrNotBusinessAccount):
		ctx.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	case err != nil:
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "success", "data": profile})
}
