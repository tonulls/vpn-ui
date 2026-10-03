package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

type DonationEntry struct {
	Name   string `json:"name"`
	Wallet string `json:"wallet"`
}

type DonationSettings struct {
	Enabled bool            `json:"enabled"`
	Entries []DonationEntry `json:"entries"`
}

const maxDonationEntries = 30

func (s *SettingService) GetDonationSettings() (DonationSettings, error) {
	var settings DonationSettings
	raw, err := s.getString("donationSettings")
	if err != nil {
		return settings, err
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, fmt.Errorf("parse donation settings: %w", err)
	}
	return normalizeDonationSettings(settings)
}

func (s *SettingService) SetDonationSettings(settings DonationSettings) error {
	normalized, err := normalizeDonationSettings(settings)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return s.setString("donationSettings", string(raw))
}

func normalizeDonationSettings(settings DonationSettings) (DonationSettings, error) {
	if len(settings.Entries) > maxDonationEntries {
		return DonationSettings{}, fmt.Errorf("at most %d donation entries are allowed", maxDonationEntries)
	}
	entries := make([]DonationEntry, 0, len(settings.Entries))
	for i, entry := range settings.Entries {
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Wallet = strings.TrimSpace(entry.Wallet)
		if entry.Name == "" && entry.Wallet == "" {
			continue
		}
		if len(entry.Name) > 80 {
			return DonationSettings{}, fmt.Errorf("donation entry %d name is longer than 80 characters", i+1)
		}
		if len(entry.Wallet) > 256 {
			return DonationSettings{}, fmt.Errorf("donation entry %d wallet is longer than 256 characters", i+1)
		}
		if strings.ContainsAny(entry.Name, "\r\n") {
			return DonationSettings{}, fmt.Errorf("donation entry %d name cannot contain a line break", i+1)
		}
		if strings.ContainsAny(entry.Wallet, " \t\r\n") {
			return DonationSettings{}, fmt.Errorf("donation entry %d wallet cannot contain whitespace", i+1)
		}
		if settings.Enabled && (entry.Name == "" || entry.Wallet == "") {
			return DonationSettings{}, fmt.Errorf("donation entry %d needs both a name and a wallet", i+1)
		}
		entries = append(entries, entry)
	}
	if settings.Enabled && len(entries) == 0 {
		return DonationSettings{}, fmt.Errorf("add at least one complete donation entry before enabling donations")
	}
	settings.Entries = entries
	return settings, nil
}
