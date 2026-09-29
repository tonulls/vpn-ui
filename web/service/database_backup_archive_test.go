package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestCreateAndDecodeDatabaseBackupArchivePlain(t *testing.T) {
	databaseBytes := testSQLiteDatabaseBytes()
	createdAt := time.Date(2026, 9, 28, 23, 19, 28, 313893000, time.FixedZone("test", 3*60*60))
	archive, wantManifest, err := CreateDatabaseBackupArchive(databaseBytes, DatabaseBackupArchiveOptions{
		AppVersion: "1.9.4.10",
		CreatedAt:  createdAt,
	})
	if err != nil {
		t.Fatalf("CreateDatabaseBackupArchive: %v", err)
	}
	if bytes.HasPrefix(archive, []byte(backupEnvelopeMagic)) {
		t.Fatal("plain archive unexpectedly has an encrypted envelope")
	}
	if wantManifest.Encrypted {
		t.Fatal("plain archive manifest is marked encrypted")
	}
	if wantManifest.BackupIntervalHours != 24 {
		t.Fatalf("default backup interval = %d, want 24 hours", wantManifest.BackupIntervalHours)
	}

	gotDatabase, gotManifest, err := DecodeDatabaseBackupArchive(archive, "")
	if err != nil {
		t.Fatalf("DecodeDatabaseBackupArchive: %v", err)
	}
	if !bytes.Equal(gotDatabase, databaseBytes) {
		t.Fatal("decoded database bytes differ from input")
	}
	if gotManifest != wantManifest {
		t.Fatalf("decoded manifest = %+v, want %+v", gotManifest, wantManifest)
	}
	if gotManifest.CreatedAt.Location() != time.UTC {
		t.Fatalf("manifest creation time location = %v, want UTC", gotManifest.CreatedAt.Location())
	}
}

func TestDecodeV1ArchiveWithoutIntervalUsesDefault(t *testing.T) {
	databaseBytes := testSQLiteDatabaseBytes()
	_, manifest, err := CreateDatabaseBackupArchive(databaseBytes, DatabaseBackupArchiveOptions{
		AppVersion: "test-version",
		CreatedAt:  time.Date(2026, 9, 28, 23, 19, 28, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateDatabaseBackupArchive: %v", err)
	}
	manifestBytes := marshalTestManifest(t, manifest)
	legacyField := []byte(`,"backupIntervalHours":24`)
	manifestBytes = bytes.Replace(manifestBytes, legacyField, nil, 1)
	if bytes.Contains(manifestBytes, []byte("backupIntervalHours")) {
		t.Fatal("failed to construct a legacy v1 manifest without backupIntervalHours")
	}
	archive := testDatabaseBackupZip(t, map[string][]byte{
		DatabaseBackupFilename:         databaseBytes,
		DatabaseBackupManifestFilename: manifestBytes,
	})
	_, decodedManifest, err := DecodeDatabaseBackupArchive(archive, "")
	if err != nil {
		t.Fatalf("DecodeDatabaseBackupArchive: %v", err)
	}
	if decodedManifest.BackupIntervalHours != 24 {
		t.Fatalf("legacy v1 backup interval = %d, want default 24", decodedManifest.BackupIntervalHours)
	}
}

func TestCreateAndDecodeDatabaseBackupArchiveEncrypted(t *testing.T) {
	databaseBytes := testSQLiteDatabaseBytes()
	archive, manifest, err := CreateDatabaseBackupArchive(databaseBytes, DatabaseBackupArchiveOptions{
		AppVersion: "test-version",
		CreatedAt:  time.Date(2026, 9, 28, 23, 19, 28, 0, time.UTC),
		Password:   "unit-test-password",
	})
	if err != nil {
		t.Fatalf("CreateDatabaseBackupArchive: %v", err)
	}
	if !bytes.HasPrefix(archive, []byte(backupEnvelopeMagic)) {
		t.Fatalf("encrypted archive does not begin with %q", backupEnvelopeMagic)
	}
	if !manifest.Encrypted {
		t.Fatal("encrypted archive manifest is not marked encrypted")
	}

	gotDatabase, gotManifest, err := DecodeDatabaseBackupArchive(archive, "unit-test-password")
	if err != nil {
		t.Fatalf("DecodeDatabaseBackupArchive: %v", err)
	}
	if !bytes.Equal(gotDatabase, databaseBytes) {
		t.Fatal("decoded database bytes differ from input")
	}
	if gotManifest != manifest {
		t.Fatalf("decoded manifest = %+v, want %+v", gotManifest, manifest)
	}
}

func TestDecodeDatabaseBackupArchiveRequiresCorrectPassword(t *testing.T) {
	archive, _, err := CreateDatabaseBackupArchive(testSQLiteDatabaseBytes(), DatabaseBackupArchiveOptions{
		AppVersion: "test-version",
		CreatedAt:  time.Now().UTC(),
		Password:   "right-password",
	})
	if err != nil {
		t.Fatalf("CreateDatabaseBackupArchive: %v", err)
	}
	if _, _, err := DecodeDatabaseBackupArchive(archive, ""); !errors.Is(err, ErrDatabaseBackupPasswordRequired) {
		t.Fatalf("empty password error = %v, want ErrDatabaseBackupPasswordRequired", err)
	}
	if _, _, err := DecodeDatabaseBackupArchive(archive, "wrong-password"); !errors.Is(err, ErrDatabaseBackupInvalidPassword) {
		t.Fatalf("wrong password error = %v, want ErrDatabaseBackupInvalidPassword", err)
	}
}

func TestDecodeDatabaseBackupArchiveRejectsEncryptedTampering(t *testing.T) {
	archive, _, err := CreateDatabaseBackupArchive(testSQLiteDatabaseBytes(), DatabaseBackupArchiveOptions{
		AppVersion: "test-version",
		CreatedAt:  time.Now().UTC(),
		Password:   "unit-test-password",
	})
	if err != nil {
		t.Fatalf("CreateDatabaseBackupArchive: %v", err)
	}
	archive[len(archive)-1] ^= 0x01
	if _, _, err := DecodeDatabaseBackupArchive(archive, "unit-test-password"); !errors.Is(err, ErrDatabaseBackupInvalidPassword) {
		t.Fatalf("tampered archive error = %v, want ErrDatabaseBackupInvalidPassword", err)
	}
}

func TestDecodeDatabaseBackupArchiveRejectsCorruptDatabaseHash(t *testing.T) {
	databaseBytes := testSQLiteDatabaseBytes()
	manifest := testDatabaseBackupManifest(databaseBytes)
	manifest.DatabaseSHA256 = strings.Repeat("0", 64)
	archive := testDatabaseBackupZip(t, map[string][]byte{
		DatabaseBackupFilename:         databaseBytes,
		DatabaseBackupManifestFilename: marshalTestManifest(t, manifest),
	})
	if _, _, err := DecodeDatabaseBackupArchive(archive, ""); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("corrupt hash error = %v, want a SHA-256 mismatch", err)
	}
}

func TestDecodeDatabaseBackupArchiveRejectsUnexpectedMember(t *testing.T) {
	databaseBytes := testSQLiteDatabaseBytes()
	manifest := testDatabaseBackupManifest(databaseBytes)
	archive := testDatabaseBackupZip(t, map[string][]byte{
		DatabaseBackupFilename:         databaseBytes,
		DatabaseBackupManifestFilename: marshalTestManifest(t, manifest),
		"extra.txt":                    []byte("not allowed"),
	})
	if _, _, err := DecodeDatabaseBackupArchive(archive, ""); err == nil {
		t.Fatal("decoder accepted an unexpected ZIP member")
	}
}

func TestDecodeDatabaseBackupArchiveRejectsUnsafeZipPath(t *testing.T) {
	databaseBytes := testSQLiteDatabaseBytes()
	manifest := testDatabaseBackupManifest(databaseBytes)
	archive := testDatabaseBackupZip(t, map[string][]byte{
		"../vpn-ui.db":                 databaseBytes,
		DatabaseBackupManifestFilename: marshalTestManifest(t, manifest),
	})
	if _, _, err := DecodeDatabaseBackupArchive(archive, ""); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe path error = %v, want unsafe path rejection", err)
	}
}

func TestDecodeDatabaseBackupArchiveRejectsUnknownEnvelopeVersion(t *testing.T) {
	if _, _, err := DecodeDatabaseBackupArchive([]byte("MTPB2"+strings.Repeat("x", 64)), "password"); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("unknown envelope error = %v, want version rejection", err)
	}
}

func TestCreateDatabaseBackupArchiveRequiresSQLiteAndMetadata(t *testing.T) {
	validOptions := DatabaseBackupArchiveOptions{
		AppVersion: "test-version",
		CreatedAt:  time.Now().UTC(),
	}
	if _, _, err := CreateDatabaseBackupArchive([]byte("not a database"), validOptions); err == nil {
		t.Fatal("CreateDatabaseBackupArchive accepted non-SQLite bytes")
	}
	if _, _, err := CreateDatabaseBackupArchive(testSQLiteDatabaseBytes(), DatabaseBackupArchiveOptions{CreatedAt: validOptions.CreatedAt}); err == nil {
		t.Fatal("CreateDatabaseBackupArchive accepted a missing app version")
	}
	if _, _, err := CreateDatabaseBackupArchive(testSQLiteDatabaseBytes(), DatabaseBackupArchiveOptions{AppVersion: validOptions.AppVersion}); err == nil {
		t.Fatal("CreateDatabaseBackupArchive accepted a missing creation time")
	}
	invalidInterval := validOptions
	invalidInterval.BackupIntervalHours = 8761
	if _, _, err := CreateDatabaseBackupArchive(testSQLiteDatabaseBytes(), invalidInterval); err == nil {
		t.Fatal("CreateDatabaseBackupArchive accepted an invalid backup interval")
	}
}

func testSQLiteDatabaseBytes() []byte {
	data := make([]byte, sqliteHeaderSize)
	copy(data, []byte(sqliteHeader))
	return data
}

func testDatabaseBackupManifest(databaseBytes []byte) DatabaseBackupManifest {
	return DatabaseBackupManifest{
		Format:              DatabaseBackupArchiveFormat,
		FormatVersion:       DatabaseBackupArchiveFormatVersion,
		AppVersion:          "test-version",
		CreatedAt:           time.Date(2026, 9, 28, 23, 19, 28, 0, time.UTC),
		BackupIntervalHours: 24,
		DatabaseFile:        DatabaseBackupFilename,
		DatabaseSize:        int64(len(databaseBytes)),
		DatabaseSHA256:      strings.Repeat("0", 64),
	}
}

func marshalTestManifest(t *testing.T, manifest DatabaseBackupManifest) []byte {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return data
}

func testDatabaseBackupZip(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, data := range members {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create test ZIP member %q: %v", name, err)
		}
		if _, err := io.Copy(entry, bytes.NewReader(data)); err != nil {
			t.Fatalf("write test ZIP member %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close test ZIP: %v", err)
	}
	return output.Bytes()
}
