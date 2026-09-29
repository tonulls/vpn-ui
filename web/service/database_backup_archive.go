package service

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// DatabaseBackupArchiveFormatVersion changes when the ZIP manifest contract changes.
	DatabaseBackupArchiveFormatVersion = 1
	DatabaseBackupArchiveFormat        = "vpn-ui-database-backup"
	DatabaseBackupFilename             = "vpn-ui.db"
	DatabaseBackupManifestFilename     = "manifest.json"

	// The in-memory helper deliberately bounds both creation and decoding. This
	// leaves room for the ZIP directory and manifest while preventing a crafted
	// upload from expanding into an unbounded allocation.
	MaxDatabaseBackupDatabaseBytes = 128 << 20
	MaxDatabaseBackupArchiveBytes  = MaxDatabaseBackupDatabaseBytes + 2<<20

	maxDatabaseBackupManifestBytes = 64 << 10
	maxDatabaseBackupMemberCount   = 2

	backupEnvelopeMagic = "MTPB1"
	backupSaltBytes     = 16
	backupNonceBytes    = 12
	backupGCMTagBytes   = 16

	// MTPB1 fixes these Argon2id parameters. A future parameter/KDF change must
	// use a new envelope version rather than interpreting old envelopes differently.
	backupArgon2Time    = 3
	backupArgon2Memory  = 64 * 1024 // KiB
	backupArgon2Threads = 2
	backupKeyBytes      = 32
)

var (
	ErrDatabaseBackupPasswordRequired = errors.New("database backup password is required")
	ErrDatabaseBackupInvalidPassword  = errors.New("database backup password is wrong or the archive is damaged")
)

// DatabaseBackupArchiveOptions controls the metadata and optional outer encryption
// used by CreateDatabaseBackupArchive.
type DatabaseBackupArchiveOptions struct {
	AppVersion          string
	CreatedAt           time.Time
	BackupIntervalHours int
	Password            string
}

// DatabaseBackupManifest describes the single database file in an archive.
type DatabaseBackupManifest struct {
	Format              string    `json:"format"`
	FormatVersion       int       `json:"formatVersion"`
	AppVersion          string    `json:"appVersion"`
	CreatedAt           time.Time `json:"createdAt"`
	BackupIntervalHours int       `json:"backupIntervalHours"`
	DatabaseFile        string    `json:"databaseFile"`
	DatabaseSize        int64     `json:"databaseSize"`
	DatabaseSHA256      string    `json:"databaseSha256"`
	Encrypted           bool      `json:"encrypted"`
}

// CreateDatabaseBackupArchive creates a ZIP containing exactly vpn-ui.db and
// manifest.json. If opts.Password is non-empty, it encrypts the complete ZIP in
// an MTPB1 envelope (Argon2id + AES-256-GCM); otherwise it returns the plain ZIP.
//
// The input must have a SQLite database header. Full SQLite integrity checks belong
// to the restore path, before the database is installed.
func CreateDatabaseBackupArchive(databaseBytes []byte, opts DatabaseBackupArchiveOptions) ([]byte, DatabaseBackupManifest, error) {
	if len(databaseBytes) < sqliteHeaderSize || !bytes.HasPrefix(databaseBytes, []byte(sqliteHeader)) {
		return nil, DatabaseBackupManifest{}, errors.New("database backup input is not a SQLite database")
	}
	if len(databaseBytes) > MaxDatabaseBackupDatabaseBytes {
		return nil, DatabaseBackupManifest{}, fmt.Errorf("database backup exceeds the %d-byte limit", MaxDatabaseBackupDatabaseBytes)
	}
	if strings.TrimSpace(opts.AppVersion) == "" {
		return nil, DatabaseBackupManifest{}, errors.New("database backup app version is required")
	}
	if opts.CreatedAt.IsZero() {
		return nil, DatabaseBackupManifest{}, errors.New("database backup creation time is required")
	}
	if opts.BackupIntervalHours == 0 {
		opts.BackupIntervalHours = 24
	}
	if opts.BackupIntervalHours < 1 || opts.BackupIntervalHours > 8760 {
		return nil, DatabaseBackupManifest{}, errors.New("database backup interval must be between 1 and 8760 hours")
	}

	digest := sha256.Sum256(databaseBytes)
	manifest := DatabaseBackupManifest{
		Format:              DatabaseBackupArchiveFormat,
		FormatVersion:       DatabaseBackupArchiveFormatVersion,
		AppVersion:          opts.AppVersion,
		CreatedAt:           opts.CreatedAt.UTC(),
		BackupIntervalHours: opts.BackupIntervalHours,
		DatabaseFile:        DatabaseBackupFilename,
		DatabaseSize:        int64(len(databaseBytes)),
		DatabaseSHA256:      hex.EncodeToString(digest[:]),
		Encrypted:           opts.Password != "",
	}
	archive, err := createDatabaseBackupZip(databaseBytes, manifest)
	if err != nil {
		return nil, DatabaseBackupManifest{}, err
	}
	if opts.Password == "" {
		return archive, manifest, nil
	}
	encrypted, err := encryptDatabaseBackupZip(archive, opts.Password)
	if err != nil {
		return nil, DatabaseBackupManifest{}, err
	}
	return encrypted, manifest, nil
}

// DecodeDatabaseBackupArchive accepts a plain .zip or an encrypted .zip.enc
// payload, verifies the complete archive contract and returns database bytes and
// the authenticated/validated manifest. Encrypted payloads require their password.
func DecodeDatabaseBackupArchive(archiveBytes []byte, password string) ([]byte, DatabaseBackupManifest, error) {
	if len(archiveBytes) == 0 || len(archiveBytes) > MaxDatabaseBackupArchiveBytes {
		return nil, DatabaseBackupManifest{}, errors.New("database backup archive is empty or exceeds the size limit")
	}

	zipBytes := archiveBytes
	encrypted := false
	if bytes.HasPrefix(archiveBytes, []byte("MTPB")) {
		if !bytes.HasPrefix(archiveBytes, []byte(backupEnvelopeMagic)) {
			return nil, DatabaseBackupManifest{}, errors.New("unsupported database backup envelope version")
		}
		if password == "" {
			return nil, DatabaseBackupManifest{}, ErrDatabaseBackupPasswordRequired
		}
		var err error
		zipBytes, err = decryptDatabaseBackupZip(archiveBytes, password)
		if err != nil {
			return nil, DatabaseBackupManifest{}, err
		}
		encrypted = true
	}

	if len(zipBytes) == 0 || len(zipBytes) > MaxDatabaseBackupArchiveBytes {
		return nil, DatabaseBackupManifest{}, errors.New("database backup ZIP is empty or exceeds the size limit")
	}
	return decodeDatabaseBackupZip(zipBytes, encrypted)
}

const (
	sqliteHeaderSize = 100
	sqliteHeader     = "SQLite format 3\x00"
)

func createDatabaseBackupZip(databaseBytes []byte, manifest DatabaseBackupManifest) ([]byte, error) {
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode database backup manifest: %w", err)
	}
	if len(manifestBytes) > maxDatabaseBackupManifestBytes {
		return nil, errors.New("database backup manifest exceeds the size limit")
	}

	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, member := range []struct {
		name string
		data []byte
	}{
		{DatabaseBackupFilename, databaseBytes},
		{DatabaseBackupManifestFilename, manifestBytes},
	} {
		header := &zip.FileHeader{Name: member.name, Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("create database backup ZIP member %q: %w", member.name, err)
		}
		if _, err := entry.Write(member.data); err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write database backup ZIP member %q: %w", member.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finish database backup ZIP: %w", err)
	}
	if output.Len() > MaxDatabaseBackupArchiveBytes {
		return nil, errors.New("database backup ZIP exceeds the archive size limit")
	}
	return output.Bytes(), nil
}

func encryptDatabaseBackupZip(zipBytes []byte, password string) ([]byte, error) {
	salt := make([]byte, backupSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate database backup salt: %w", err)
	}
	nonce := make([]byte, backupNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate database backup nonce: %w", err)
	}

	header := make([]byte, 0, len(backupEnvelopeMagic)+backupSaltBytes+backupNonceBytes)
	header = append(header, backupEnvelopeMagic...)
	header = append(header, salt...)
	header = append(header, nonce...)
	key := argon2.IDKey([]byte(password), salt, backupArgon2Time, backupArgon2Memory, backupArgon2Threads, backupKeyBytes)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize database backup cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize database backup authentication: %w", err)
	}
	sealed := aead.Seal(nil, nonce, zipBytes, header)
	output := append(header, sealed...)
	if len(output) > MaxDatabaseBackupArchiveBytes {
		return nil, errors.New("encrypted database backup exceeds the archive size limit")
	}
	return output, nil
}

func decryptDatabaseBackupZip(envelope []byte, password string) ([]byte, error) {
	headerLen := len(backupEnvelopeMagic) + backupSaltBytes + backupNonceBytes
	if len(envelope) < headerLen+backupGCMTagBytes {
		return nil, errors.New("database backup envelope is truncated")
	}
	header := envelope[:headerLen]
	saltStart := len(backupEnvelopeMagic)
	saltEnd := saltStart + backupSaltBytes
	salt := envelope[saltStart:saltEnd]
	nonce := envelope[saltEnd:headerLen]
	key := argon2.IDKey([]byte(password), salt, backupArgon2Time, backupArgon2Memory, backupArgon2Threads, backupKeyBytes)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize database backup cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize database backup authentication: %w", err)
	}
	plain, err := aead.Open(nil, nonce, envelope[headerLen:], header)
	if err != nil {
		return nil, ErrDatabaseBackupInvalidPassword
	}
	return plain, nil
}

func decodeDatabaseBackupZip(zipBytes []byte, encrypted bool) ([]byte, DatabaseBackupManifest, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, DatabaseBackupManifest{}, fmt.Errorf("invalid database backup ZIP: %w", err)
	}
	if len(reader.File) != maxDatabaseBackupMemberCount {
		return nil, DatabaseBackupManifest{}, errors.New("database backup ZIP must contain exactly vpn-ui.db and manifest.json")
	}

	members := make(map[string]*zip.File, maxDatabaseBackupMemberCount)
	var totalUncompressed uint64
	for _, member := range reader.File {
		if err := validateDatabaseBackupMemberPath(member.Name); err != nil {
			return nil, DatabaseBackupManifest{}, err
		}
		if member.FileInfo().IsDir() || !member.Mode().IsRegular() {
			return nil, DatabaseBackupManifest{}, fmt.Errorf("database backup ZIP member %q is not a regular file", member.Name)
		}
		if member.Name != DatabaseBackupFilename && member.Name != DatabaseBackupManifestFilename {
			return nil, DatabaseBackupManifest{}, fmt.Errorf("unexpected database backup ZIP member %q", member.Name)
		}
		if _, exists := members[member.Name]; exists {
			return nil, DatabaseBackupManifest{}, fmt.Errorf("duplicate database backup ZIP member %q", member.Name)
		}
		maxMemberBytes := uint64(MaxDatabaseBackupDatabaseBytes)
		if member.Name == DatabaseBackupManifestFilename {
			maxMemberBytes = maxDatabaseBackupManifestBytes
		}
		if member.UncompressedSize64 > maxMemberBytes || member.CompressedSize64 > uint64(MaxDatabaseBackupArchiveBytes) {
			return nil, DatabaseBackupManifest{}, fmt.Errorf("database backup ZIP member %q exceeds the size limit", member.Name)
		}
		totalUncompressed += member.UncompressedSize64
		if totalUncompressed > uint64(MaxDatabaseBackupDatabaseBytes)+maxDatabaseBackupManifestBytes {
			return nil, DatabaseBackupManifest{}, errors.New("database backup ZIP exceeds the total uncompressed size limit")
		}
		members[member.Name] = member
	}
	if members[DatabaseBackupFilename] == nil || members[DatabaseBackupManifestFilename] == nil {
		return nil, DatabaseBackupManifest{}, errors.New("database backup ZIP is missing a required member")
	}

	databaseBytes, err := readDatabaseBackupMember(members[DatabaseBackupFilename], MaxDatabaseBackupDatabaseBytes)
	if err != nil {
		return nil, DatabaseBackupManifest{}, err
	}
	if len(databaseBytes) < sqliteHeaderSize || !bytes.HasPrefix(databaseBytes, []byte(sqliteHeader)) {
		return nil, DatabaseBackupManifest{}, errors.New("database backup member is not a SQLite database")
	}
	manifestBytes, err := readDatabaseBackupMember(members[DatabaseBackupManifestFilename], maxDatabaseBackupManifestBytes)
	if err != nil {
		return nil, DatabaseBackupManifest{}, err
	}
	manifest, err := decodeDatabaseBackupManifest(manifestBytes)
	if err != nil {
		return nil, DatabaseBackupManifest{}, err
	}
	if manifest.Encrypted != encrypted {
		return nil, DatabaseBackupManifest{}, errors.New("database backup encryption flag does not match its envelope")
	}
	if err := validateDatabaseBackupManifest(manifest, databaseBytes); err != nil {
		return nil, DatabaseBackupManifest{}, err
	}
	return databaseBytes, manifest, nil
}

func validateDatabaseBackupMemberPath(name string) error {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) ||
		strings.HasPrefix(name, "/") || path.IsAbs(name) || path.Clean(name) != name {
		return fmt.Errorf("unsafe database backup ZIP member path %q", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe database backup ZIP member path %q", name)
		}
	}
	return nil
}

func readDatabaseBackupMember(member *zip.File, maxBytes uint64) ([]byte, error) {
	if member.UncompressedSize64 > maxBytes {
		return nil, fmt.Errorf("database backup ZIP member %q exceeds the size limit", member.Name)
	}
	r, err := member.Open()
	if err != nil {
		return nil, fmt.Errorf("open database backup ZIP member %q: %w", member.Name, err)
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read database backup ZIP member %q: %w", member.Name, err)
	}
	if uint64(len(data)) > maxBytes || uint64(len(data)) != member.UncompressedSize64 {
		return nil, fmt.Errorf("database backup ZIP member %q has an invalid size", member.Name)
	}
	return data, nil
}

func decodeDatabaseBackupManifest(data []byte) (DatabaseBackupManifest, error) {
	var manifest DatabaseBackupManifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return DatabaseBackupManifest{}, fmt.Errorf("invalid database backup manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return DatabaseBackupManifest{}, errors.New("database backup manifest contains trailing JSON")
		}
		return DatabaseBackupManifest{}, fmt.Errorf("invalid trailing data in database backup manifest: %w", err)
	}
	// Early v1 archives did not carry the interval field; they used the original
	// 24-hour default. Keep those archives restorable while writing the field in all
	// new manifests.
	if manifest.BackupIntervalHours == 0 {
		manifest.BackupIntervalHours = 24
	}
	return manifest, nil
}

func validateDatabaseBackupManifest(manifest DatabaseBackupManifest, databaseBytes []byte) error {
	if manifest.Format != DatabaseBackupArchiveFormat {
		return fmt.Errorf("unsupported database backup format %q", manifest.Format)
	}
	if manifest.FormatVersion != DatabaseBackupArchiveFormatVersion {
		return fmt.Errorf("unsupported database backup format version %d", manifest.FormatVersion)
	}
	if strings.TrimSpace(manifest.AppVersion) == "" || manifest.CreatedAt.IsZero() {
		return errors.New("database backup manifest is missing app version or creation time")
	}
	if manifest.BackupIntervalHours < 1 || manifest.BackupIntervalHours > 8760 {
		return errors.New("database backup manifest has an invalid backup interval")
	}
	if manifest.DatabaseFile != DatabaseBackupFilename {
		return fmt.Errorf("unexpected database filename %q in manifest", manifest.DatabaseFile)
	}
	if manifest.DatabaseSize != int64(len(databaseBytes)) {
		return errors.New("database backup manifest size does not match the database member")
	}
	decodedHash, err := hex.DecodeString(manifest.DatabaseSHA256)
	if err != nil || len(decodedHash) != sha256.Size {
		return errors.New("database backup manifest contains an invalid SHA-256 value")
	}
	digest := sha256.Sum256(databaseBytes)
	if !bytes.Equal(decodedHash, digest[:]) {
		return errors.New("database backup SHA-256 does not match the manifest")
	}
	return nil
}
