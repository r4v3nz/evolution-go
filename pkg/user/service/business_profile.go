// Fork change (r4v3nz/evolution-go): this file is not part of upstream Evolution Go.
//
// Reads and edits the WhatsApp Business profile (description, address, e-mail,
// websites, business hours) of the instance's own account. whatsmeow cannot
// write it and its GetBusinessProfile skips description and websites, so both
// directions use the w:biz IQ directly, like WhatsApp Business Web. This is a
// plain server request, not an app state patch.

package user_service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Limits of the WhatsApp Business app.
const (
	businessDescriptionMax = 512
	businessAddressMax     = 256
	businessEmailMax       = 128
	businessWebsiteMax     = 256
	businessWebsitesMax    = 2
)

var (
	ErrNotBusinessAccount     = errors.New("this WhatsApp account is not a WhatsApp Business account")
	ErrInvalidBusinessProfile = errors.New("invalid business profile")
)

var businessDays = map[string]bool{"sun": true, "mon": true, "tue": true, "wed": true, "thu": true, "fri": true, "sat": true}

// BusinessHoursDay is one open day; a day that is not listed is closed.
type BusinessHoursDay struct {
	Day  string `json:"day"`  // sun, mon, tue, wed, thu, fri, sat
	Mode string `json:"mode"` // specific_hours, open_24h, appointment_only
	// Minutes after midnight, only for specific_hours.
	OpenTime  *int `json:"openTime,omitempty"`
	CloseTime *int `json:"closeTime,omitempty"`
}

type BusinessHours struct {
	Timezone string             `json:"timezone"`
	Days     []BusinessHoursDay `json:"days"`
}

type BusinessProfile struct {
	IsBusiness  bool           `json:"isBusiness"`
	Description string         `json:"description"`
	Address     string         `json:"address"`
	Email       string         `json:"email"`
	Websites    []string       `json:"websites"`
	Categories  []string       `json:"categories"`
	Hours       *BusinessHours `json:"hours"`
}

// SetBusinessProfileStruct changes only the fields that are present; an empty
// string or list clears the field.
type SetBusinessProfileStruct struct {
	Description *string        `json:"description"`
	Address     *string        `json:"address"`
	Email       *string        `json:"email"`
	Websites    *[]string      `json:"websites"`
	Hours       *BusinessHours `json:"hours"`
}

func (u *userService) GetBusinessProfile(instance *instance_model.Instance) (*BusinessProfile, error) {
	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	return fetchBusinessProfile(client)
}

func (u *userService) SetBusinessProfile(data *SetBusinessProfileStruct, instance *instance_model.Instance) (*BusinessProfile, error) {
	if err := validateBusinessProfile(data); err != nil {
		return nil, err
	}

	client, err := u.ensureClientConnected(instance.Id)
	if err != nil {
		return nil, err
	}

	// A regular WhatsApp account has no business profile to edit.
	current, err := fetchBusinessProfile(client)
	if err != nil {
		return nil, err
	}
	if !current.IsBusiness {
		return nil, ErrNotBusinessAccount
	}

	fields := businessProfileFields(data)
	if len(fields) == 0 {
		return current, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:biz",
		Type:      whatsmeow.DangerousInfoQueryType("set"),
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag:     "business_profile",
			Attrs:   waBinary.Attrs{"v": "3", "mutation_type": "delta"},
			Content: fields,
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to update business profile: %w", err)
	}

	u.loggerWrapper.GetLogger(instance.Id).LogInfo("[%s] Business profile updated", instance.Id)
	return fetchBusinessProfile(client)
}

func fetchBusinessProfile(client *whatsmeow.Client) (*BusinessProfile, error) {
	if client.Store.ID == nil {
		return nil, errors.New("instance is not logged in")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:biz",
		Type:      whatsmeow.DangerousInfoQueryType("get"),
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag:   "business_profile",
			Attrs: waBinary.Attrs{"v": "244"},
			Content: []waBinary.Node{{
				Tag:   "profile",
				Attrs: waBinary.Attrs{"jid": client.Store.ID.ToNonAD()},
			}},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get business profile: %w", err)
	}

	return parseBusinessProfile(resp), nil
}

// parseBusinessProfile reads the w:biz answer. Only a business account has a
// profile element, so its absence means a regular WhatsApp account.
func parseBusinessProfile(resp *waBinary.Node) *BusinessProfile {
	empty := &BusinessProfile{Websites: []string{}, Categories: []string{}}
	if resp == nil {
		return empty
	}
	profileContainer, ok := resp.GetOptionalChildByTag("business_profile")
	if !ok {
		return empty
	}
	profile, ok := profileContainer.GetOptionalChildByTag("profile")
	if !ok || profile.AttrGetter().OptionalJID("jid") == nil {
		return empty
	}

	result := &BusinessProfile{
		IsBusiness:  true,
		Description: nodeText(profile, "description"),
		Address:     nodeText(profile, "address"),
		Email:       nodeText(profile, "email"),
		Websites:    []string{},
		Categories:  []string{},
	}
	for _, website := range profile.GetChildrenByTag("website") {
		if text := strings.TrimSpace(contentText(website)); text != "" {
			result.Websites = append(result.Websites, text)
		}
	}
	if categories, ok := profile.GetOptionalChildByTag("categories"); ok {
		for _, category := range categories.GetChildrenByTag("category") {
			if name := strings.TrimSpace(contentText(category)); name != "" {
				result.Categories = append(result.Categories, name)
			}
		}
	}
	if hours, ok := profile.GetOptionalChildByTag("business_hours"); ok {
		result.Hours = &BusinessHours{Timezone: hours.AttrGetter().OptionalString("timezone"), Days: []BusinessHoursDay{}}
		for _, config := range hours.GetChildrenByTag("business_hours_config") {
			attrs := config.AttrGetter()
			day := BusinessHoursDay{Day: attrs.OptionalString("day_of_week"), Mode: attrs.OptionalString("mode")}
			if day.Mode == "specific_hours" {
				day.OpenTime = optionalMinutes(attrs.OptionalString("open_time"))
				day.CloseTime = optionalMinutes(attrs.OptionalString("close_time"))
			}
			result.Hours.Days = append(result.Hours.Days, day)
		}
	}

	return result
}

// businessProfileFields builds the children of a delta update: only the
// fields that are present in the request. Free text goes as bytes, as in
// whatsmeow, so it is never packed as a dictionary token.
func businessProfileFields(data *SetBusinessProfileStruct) []waBinary.Node {
	fields := []waBinary.Node{}
	text := func(tag string, value *string) {
		if value != nil {
			fields = append(fields, waBinary.Node{Tag: tag, Content: []byte(strings.TrimSpace(*value))})
		}
	}
	text("description", data.Description)
	text("address", data.Address)
	text("email", data.Email)

	if data.Websites != nil {
		websites := *data.Websites
		if len(websites) == 0 {
			// Clears the websites.
			fields = append(fields, waBinary.Node{Tag: "website", Content: []byte{}})
		}
		for _, website := range websites {
			fields = append(fields, waBinary.Node{Tag: "website", Content: []byte(strings.TrimSpace(website))})
		}
	}

	if data.Hours != nil {
		configs := make([]waBinary.Node, 0, len(data.Hours.Days))
		for _, day := range data.Hours.Days {
			attrs := waBinary.Attrs{"day_of_week": day.Day, "mode": day.Mode}
			if day.Mode == "specific_hours" {
				attrs["open_time"] = strconv.Itoa(*day.OpenTime)
				attrs["close_time"] = strconv.Itoa(*day.CloseTime)
			}
			configs = append(configs, waBinary.Node{Tag: "business_hours_config", Attrs: attrs})
		}
		fields = append(fields, waBinary.Node{
			Tag:     "business_hours",
			Attrs:   waBinary.Attrs{"timezone": data.Hours.Timezone},
			Content: configs,
		})
	}

	return fields
}

func validateBusinessProfile(data *SetBusinessProfileStruct) error {
	if err := checkBusinessProfile(data); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidBusinessProfile, err)
	}
	return nil
}

func checkBusinessProfile(data *SetBusinessProfileStruct) error {
	if data == nil {
		return errors.New("business profile is required")
	}
	if data.Description != nil && utf8.RuneCountInString(strings.TrimSpace(*data.Description)) > businessDescriptionMax {
		return fmt.Errorf("description must have at most %d characters", businessDescriptionMax)
	}
	if data.Address != nil && utf8.RuneCountInString(strings.TrimSpace(*data.Address)) > businessAddressMax {
		return fmt.Errorf("address must have at most %d characters", businessAddressMax)
	}
	if data.Email != nil {
		email := strings.TrimSpace(*data.Email)
		if len(email) > businessEmailMax {
			return fmt.Errorf("email must have at most %d characters", businessEmailMax)
		}
		if email != "" {
			if parsed, err := mail.ParseAddress(email); err != nil || parsed.Address != email {
				return errors.New("email is not a valid address")
			}
		}
	}
	if data.Websites != nil {
		if len(*data.Websites) > businessWebsitesMax {
			return fmt.Errorf("at most %d websites are allowed", businessWebsitesMax)
		}
		for _, website := range *data.Websites {
			website = strings.TrimSpace(website)
			parsed, err := url.ParseRequestURI(website)
			if len(website) > businessWebsiteMax || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return fmt.Errorf("website %q must be an http(s) address with at most %d characters", website, businessWebsiteMax)
			}
		}
	}
	if data.Hours != nil {
		if _, err := time.LoadLocation(data.Hours.Timezone); err != nil || data.Hours.Timezone == "" {
			return fmt.Errorf("unknown business hours timezone %q", data.Hours.Timezone)
		}
		seen := map[string]bool{}
		for _, day := range data.Hours.Days {
			if !businessDays[day.Day] || seen[day.Day] {
				return fmt.Errorf("invalid or repeated business hours day %q", day.Day)
			}
			seen[day.Day] = true
			switch day.Mode {
			case "open_24h", "appointment_only":
			case "specific_hours":
				if day.OpenTime == nil || day.CloseTime == nil || *day.OpenTime < 0 || *day.CloseTime > 1440 || *day.OpenTime >= *day.CloseTime {
					return fmt.Errorf("business hours of %q need an opening time before the closing time (minutes 0-1440)", day.Day)
				}
			default:
				return fmt.Errorf("invalid business hours mode %q", day.Mode)
			}
		}
	}

	return nil
}

func nodeText(node waBinary.Node, tag string) string {
	child, ok := node.GetOptionalChildByTag(tag)
	if !ok {
		return ""
	}
	return strings.TrimSpace(contentText(child))
}

func contentText(node waBinary.Node) string {
	switch content := node.Content.(type) {
	case []byte:
		return string(content)
	case string:
		return content
	default:
		return ""
	}
}

func optionalMinutes(value string) *int {
	minutes, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	return &minutes
}
