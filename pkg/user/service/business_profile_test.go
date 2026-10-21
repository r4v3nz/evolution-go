// Fork change (r4v3nz/evolution-go): this file is not part of upstream Evolution Go.

package user_service

import (
	"errors"
	"strings"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func ptr[T any](v T) *T { return &v }

func businessResponse(profile ...waBinary.Node) *waBinary.Node {
	return &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{Tag: "business_profile", Content: profile}}}
}

func TestParseBusinessProfileReadsEveryField(t *testing.T) {
	resp := businessResponse(waBinary.Node{
		Tag:   "profile",
		Attrs: waBinary.Attrs{"jid": types.NewJID("5511999990000", types.DefaultUserServer)},
		Content: []waBinary.Node{
			{Tag: "description", Content: []byte("Crédito consignado")},
			{Tag: "address", Content: []byte("Rua A, 10")},
			{Tag: "email", Content: []byte("contato@argon.test")},
			{Tag: "website", Content: []byte("https://argon.test")},
			{Tag: "website", Content: []byte("https://argon.test/inss")},
			{Tag: "categories", Content: []waBinary.Node{{Tag: "category", Attrs: waBinary.Attrs{"id": "1"}, Content: []byte("Serviços financeiros")}}},
			{Tag: "business_hours", Attrs: waBinary.Attrs{"timezone": "America/Sao_Paulo"}, Content: []waBinary.Node{
				{Tag: "business_hours_config", Attrs: waBinary.Attrs{"day_of_week": "mon", "mode": "specific_hours", "open_time": "540", "close_time": "1080"}},
				{Tag: "business_hours_config", Attrs: waBinary.Attrs{"day_of_week": "sat", "mode": "appointment_only"}},
			}},
		},
	})

	profile := parseBusinessProfile(resp)

	if !profile.IsBusiness || profile.Description != "Crédito consignado" || profile.Address != "Rua A, 10" || profile.Email != "contato@argon.test" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	if len(profile.Websites) != 2 || profile.Websites[1] != "https://argon.test/inss" {
		t.Fatalf("websites = %v", profile.Websites)
	}
	if len(profile.Categories) != 1 || profile.Categories[0] != "Serviços financeiros" {
		t.Fatalf("categories = %v", profile.Categories)
	}
	if profile.Hours == nil || profile.Hours.Timezone != "America/Sao_Paulo" || len(profile.Hours.Days) != 2 {
		t.Fatalf("hours = %+v", profile.Hours)
	}
	monday := profile.Hours.Days[0]
	if monday.Day != "mon" || *monday.OpenTime != 540 || *monday.CloseTime != 1080 {
		t.Fatalf("monday = %+v", monday)
	}
	if saturday := profile.Hours.Days[1]; saturday.Mode != "appointment_only" || saturday.OpenTime != nil {
		t.Fatalf("saturday = %+v", saturday)
	}
}

func TestParseBusinessProfileWithoutProfileIsRegularAccount(t *testing.T) {
	for name, resp := range map[string]*waBinary.Node{
		"no response":         nil,
		"no business_profile": {Tag: "iq"},
		"no profile":          businessResponse(),
		"profile without jid": businessResponse(waBinary.Node{Tag: "profile"}),
	} {
		profile := parseBusinessProfile(resp)
		if profile.IsBusiness || profile.Websites == nil || profile.Categories == nil {
			t.Errorf("%s: want a regular account with empty lists, got %+v", name, profile)
		}
	}
}

func TestBusinessProfileFieldsSendsOnlyPresentFields(t *testing.T) {
	fields := businessProfileFields(&SetBusinessProfileStruct{
		Description: ptr("  Nova descrição  "),
		Websites:    &[]string{"https://argon.test"},
		Hours: &BusinessHours{Timezone: "America/Sao_Paulo", Days: []BusinessHoursDay{
			{Day: "mon", Mode: "specific_hours", OpenTime: ptr(480), CloseTime: ptr(1020)},
			{Day: "sun", Mode: "open_24h"},
		}},
	})

	tags := []string{}
	for _, field := range fields {
		tags = append(tags, field.Tag)
	}
	if strings.Join(tags, ",") != "description,website,business_hours" {
		t.Fatalf("tags = %v", tags)
	}
	if string(fields[0].Content.([]byte)) != "Nova descrição" {
		t.Fatalf("description = %q", fields[0].Content)
	}
	configs := fields[2].Content.([]waBinary.Node)
	if fields[2].Attrs["timezone"] != "America/Sao_Paulo" || configs[0].Attrs["open_time"] != "480" || configs[0].Attrs["close_time"] != "1020" {
		t.Fatalf("hours = %+v", fields[2])
	}
	if _, ok := configs[1].Attrs["open_time"]; ok {
		t.Fatalf("open_24h must not carry times: %+v", configs[1])
	}
}

func TestBusinessProfileFieldsClearsWithEmptyValues(t *testing.T) {
	fields := businessProfileFields(&SetBusinessProfileStruct{Email: ptr(""), Websites: &[]string{}})

	if len(fields) != 2 || fields[0].Tag != "email" || len(fields[0].Content.([]byte)) != 0 || fields[1].Tag != "website" || len(fields[1].Content.([]byte)) != 0 {
		t.Fatalf("fields = %+v", fields)
	}
	if len(businessProfileFields(&SetBusinessProfileStruct{})) != 0 {
		t.Fatal("an empty request must send nothing")
	}
}

func TestValidateBusinessProfile(t *testing.T) {
	valid := &SetBusinessProfileStruct{
		Description: ptr(strings.Repeat("a", businessDescriptionMax)),
		Email:       ptr("contato@argon.test"),
		Websites:    &[]string{"https://argon.test", "http://argon.test/inss"},
		Hours:       &BusinessHours{Timezone: "America/Sao_Paulo", Days: []BusinessHoursDay{{Day: "mon", Mode: "specific_hours", OpenTime: ptr(0), CloseTime: ptr(1440)}}},
	}
	if err := validateBusinessProfile(valid); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	if err := validateBusinessProfile(&SetBusinessProfileStruct{Email: ptr("")}); err != nil {
		t.Fatalf("clearing the e-mail rejected: %v", err)
	}

	invalid := map[string]*SetBusinessProfileStruct{
		"description too long": {Description: ptr(strings.Repeat("a", businessDescriptionMax+1))},
		"bad e-mail":           {Email: ptr("contato")},
		"three websites":       {Websites: &[]string{"https://a.test", "https://b.test", "https://c.test"}},
		"website without http": {Websites: &[]string{"argon.test"}},
		"unknown timezone":     {Hours: &BusinessHours{Timezone: "Mars/Base"}},
		"repeated day":         {Hours: &BusinessHours{Timezone: "UTC", Days: []BusinessHoursDay{{Day: "mon", Mode: "open_24h"}, {Day: "mon", Mode: "open_24h"}}}},
		"unknown day":          {Hours: &BusinessHours{Timezone: "UTC", Days: []BusinessHoursDay{{Day: "monday", Mode: "open_24h"}}}},
		"closing before open":  {Hours: &BusinessHours{Timezone: "UTC", Days: []BusinessHoursDay{{Day: "mon", Mode: "specific_hours", OpenTime: ptr(600), CloseTime: ptr(540)}}}},
		"missing times":        {Hours: &BusinessHours{Timezone: "UTC", Days: []BusinessHoursDay{{Day: "mon", Mode: "specific_hours"}}}},
		"unknown mode":         {Hours: &BusinessHours{Timezone: "UTC", Days: []BusinessHoursDay{{Day: "mon", Mode: "closed"}}}},
	}
	for name, data := range invalid {
		if err := validateBusinessProfile(data); !errors.Is(err, ErrInvalidBusinessProfile) {
			t.Errorf("%s: want ErrInvalidBusinessProfile, got %v", name, err)
		}
	}
}
