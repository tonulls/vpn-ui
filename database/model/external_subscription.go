package model

// ExternalSubscriptionSettings is the singleton presentation/probe configuration for
// the external subscription module. Slot-specific source and filtering settings live
// in ExternalSubscriptionSlot.
type ExternalSubscriptionSettings struct {
	ID            int    `json:"id" gorm:"primaryKey;autoIncrement:false"`
	DisplayName   string `json:"displayName" gorm:"not null;default:External Selector"`
	ProbeURL      string `json:"probeUrl" gorm:"not null"`
	ProbeURL2     string `json:"probeUrl2" gorm:"not null"`
	ProbeURL3     string `json:"probeUrl3" gorm:"not null"`
	ProbeURLsJSON string `json:"-" gorm:"column:probe_urls_json;type:text;not null;default:'[]'"`
	Enabled       bool   `json:"enabled" gorm:"not null;default:true"`
}

func (ExternalSubscriptionSettings) TableName() string { return "external_subscription_settings" }

// ExternalSubscriptionSlot is one independent selector over a remote list. The raw
// candidate set and selected fingerprints are private state; credential-bearing URIs
// must never be serialized to panel APIs, logs, or client-list payloads.
type ExternalSubscriptionSlot struct {
	ID                           int    `json:"id" gorm:"primaryKey;autoIncrement"`
	SortOrder                    int    `json:"sortOrder" gorm:"not null;default:0"`
	Name                         string `json:"name" gorm:"not null"`
	ShowName                     bool   `json:"showName" gorm:"column:show_name;not null;default:true"`
	SelectionMode                string `json:"selectionMode" gorm:"not null;default:auto"`
	SourceURL                    string `json:"-" gorm:"not null"`
	RefreshIntervalMinutes       int    `json:"refreshIntervalMinutes" gorm:"not null;default:30"`
	CheckIntervalMinutes         int    `json:"checkIntervalMinutes" gorm:"not null;default:5"`
	SubscriptionKeyCount         int    `json:"subscriptionKeyCount" gorm:"column:subscription_key_count;not null;default:1"`
	ForceRotationIntervalMinutes int    `json:"forceRotationIntervalMinutes" gorm:"column:force_rotation_interval_minutes;not null;default:0"`
	FilterMode                   string `json:"filterMode" gorm:"not null;default:include"`
	CountryFlagsJSON             string `json:"-" gorm:"column:country_flags_json;type:text;not null;default:'[]'"`
	Enabled                      bool   `json:"enabled" gorm:"not null;default:1"`

	CandidateDataJSON           string `json:"-" gorm:"column:candidate_data_json;type:text;not null;default:'[]'"`
	SelectedFingerprintsJSON    string `json:"-" gorm:"column:selected_fingerprints_json;type:text;not null;default:'[]'"`
	ActiveCandidatesJSON        string `json:"-" gorm:"column:active_candidates_json;type:text;not null;default:'[]'"`
	ReplacementCandidatesJSON   string `json:"-" gorm:"column:replacement_candidates_json;type:text;not null;default:'[]'"`
	RotationFingerprintsJSON    string `json:"-" gorm:"column:rotation_fingerprints_json;type:text;not null;default:'[]'"`
	RotationCandidatesJSON      string `json:"-" gorm:"column:rotation_candidates_json;type:text;not null;default:'[]'"`
	ActiveCheckFingerprintsJSON string `json:"-" gorm:"column:active_check_fingerprints_json;type:text;not null;default:'[]'"`
	ActiveCheckFailuresJSON     string `json:"-" gorm:"column:active_check_failures_json;type:text;not null;default:'[]'"`
	ActiveCheckCursor           int    `json:"-" gorm:"column:active_check_cursor;not null;default:0"`
	ActiveURI                   string `json:"-" gorm:"column:active_uri;type:text"`
	ActiveFingerprint           string `json:"-" gorm:"column:active_fingerprint;size:64"`
	SourceETag                  string `json:"-" gorm:"column:source_etag;size:512"`
	SourceLastModified          string `json:"-" gorm:"column:source_last_modified;size:256"`

	ActiveName           string `json:"activeName" gorm:"column:active_name;size:512"`
	ActiveFlag           string `json:"activeFlag" gorm:"column:active_flag;size:32"`
	Status               string `json:"status" gorm:"size:32;not null;default:pending"`
	LastError            string `json:"lastError" gorm:"column:last_error;size:512"`
	CandidateCount       int    `json:"candidateCount" gorm:"column:candidate_count;not null;default:0"`
	MatchingCount        int    `json:"matchingCount" gorm:"column:matching_count;not null;default:0"`
	CandidateCursor      int    `json:"-" gorm:"column:candidate_cursor;not null;default:0"`
	ManualRotate         bool   `json:"-" gorm:"column:manual_rotate;not null;default:false"`
	LastFetchedAt        int64  `json:"lastFetchedAt" gorm:"column:last_fetched_at;not null;default:0"`
	LastCheckedAt        int64  `json:"lastCheckedAt" gorm:"column:last_checked_at;not null;default:0"`
	NextFetchAt          int64  `json:"nextFetchAt" gorm:"column:next_fetch_at;not null;default:0"`
	NextForcedRotationAt int64  `json:"nextForcedRotationAt" gorm:"column:next_forced_rotation_at;not null;default:0"`
	NextCheckAt          int64  `json:"nextCheckAt" gorm:"column:next_check_at;not null;default:0"`
	CreatedAt            int64  `json:"createdAt" gorm:"column:created_at;autoCreateTime:milli"`
	UpdatedAt            int64  `json:"updatedAt" gorm:"column:updated_at;autoUpdateTime:milli"`
}

func (ExternalSubscriptionSlot) TableName() string { return "external_subscription_slots" }
