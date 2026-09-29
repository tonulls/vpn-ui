package entity

import "testing"

func TestNormalizeForumChatID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		bad  bool
	}{
		{name: "unsigned supergroup ID", in: "1001234567890", want: "-1001234567890"},
		{name: "canonical negative ID", in: "-1001234567890", want: "-1001234567890"},
		{name: "surrounding whitespace", in: " 1001234567890 ", want: "-1001234567890"},
		{name: "empty", in: "", want: ""},
		{name: "ordinary group ID", in: "12345", bad: true},
		{name: "double sign", in: "--1001234567890", bad: true},
		{name: "non-numeric", in: "-100topic", bad: true},
		{name: "overflow", in: "100999999999999999999999", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeForumChatID(tt.in)
			if tt.bad {
				if err == nil {
					t.Fatalf("normalizeForumChatID(%q) unexpectedly succeeded with %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeForumChatID(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeForumChatID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAllSettingRequiresForumChatIDWhenForumEnabled(t *testing.T) {
	settings := AllSetting{
		WebPort:               2083,
		SubPort:               2097,
		TimeLocation:          "UTC",
		TgForumEnable:         true,
		TgBackupIntervalHours: 24,
		TgCpu:                 80,
	}
	if err := settings.CheckValid(); err == nil {
		t.Fatal("forum can be enabled without a chat ID")
	}
}

func TestAllSettingNormalizesForumChatID(t *testing.T) {
	settings := AllSetting{
		WebPort:               2083,
		SubPort:               2097,
		TimeLocation:          "UTC",
		TgForumEnable:         true,
		TgForumChatId:         "1001234567890",
		TgBackupIntervalHours: 24,
		TgCpu:                 80,
	}
	if err := settings.CheckValid(); err != nil {
		t.Fatalf("CheckValid: %v", err)
	}
	if settings.TgForumChatId != "-1001234567890" {
		t.Fatalf("forum chat ID = %q, want normalized negative ID", settings.TgForumChatId)
	}
}
