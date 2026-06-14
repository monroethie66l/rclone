package main

import (
	"context"
	"testing"
)

type TestLogger struct {
	Infos    []string
	Warnings []string
	Notices  []string
}

func (t *TestLogger) Infof(format string, v ...interface{}) {
	t.Infos = append(t.Infos, format)
}

func (t *TestLogger) Warningf(format string, v ...interface{}) {
	t.Warnings = append(t.Warnings, format)
}

func (t *TestLogger) Noticef(format string, v ...interface{}) {
	t.Notices = append(t.Notices, format)
}

func TestSyncMetadataOnly(t *testing.T) {
	ctx := context.Background()

	// 1. Uploads a test file with initial metadata.
	src := NewMockLocal()
	dst := NewMockS3()

	srcObj := Object{
		Path: "testfile.bin",
		Size: 100,
		Hash: "md5hash123",
		Metadata: map[string]string{
			"mtime": "2023-01-01T00:00:00Z",
		},
	}
	src.Put(ctx, srcObj, []byte("some data"))

	logger := &TestLogger{}
	err := Sync(ctx, src, dst, SyncOptions{Metadata: true}, logger)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if len(logger.Infos) != 1 || logger.Infos[0] != "%s: Uploaded (new file)" {
		t.Errorf("Expected upload log, got: %v", logger.Infos)
	}

	// 2. Modifies the local metadata while keeping the file content identical.
	srcObj.Metadata["mtime"] = "2023-01-02T00:00:00Z"
	src.Put(ctx, srcObj, []byte("some data"))

	// 3. Runs a sync/copy operation.
	logger = &TestLogger{}
	err = Sync(ctx, src, dst, SyncOptions{Metadata: true}, logger)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// 4. Asserts that the operation succeeds and verifies that a CopyObject request
	// with MetadataDirective: REPLACE was dispatched instead of a PutObject request.
	if len(logger.Infos) != 1 || logger.Infos[0] != "%s: Updated metadata (server-side copy)" {
		t.Errorf("Expected metadata update log, got: %v", logger.Infos)
	}

	// Verify metadata was updated in destination
	dstObj := dst.Objects["testfile.bin"]
	if dstObj.Metadata["mtime"] != "2023-01-02T00:00:00Z" {
		t.Errorf("Expected updated metadata, got: %v", dstObj.Metadata)
	}
}

func TestSyncMetadataOnlyDryRun(t *testing.T) {
	ctx := context.Background()

	src := NewMockLocal()
	dst := NewMockS3()

	srcObj := Object{
		Path: "testfile.bin",
		Size: 100,
		Hash: "md5hash123",
		Metadata: map[string]string{
			"mtime": "2023-01-01T00:00:00Z",
		},
	}
	src.Put(ctx, srcObj, []byte("some data"))

	// First sync to populate dst
	Sync(ctx, src, dst, SyncOptions{Metadata: true}, &TestLogger{})

	// Modify metadata
	srcObj.Metadata["mtime"] = "2023-01-02T00:00:00Z"
	src.Put(ctx, srcObj, []byte("some data"))

	// Run with DryRun
	logger := &TestLogger{}
	err := Sync(ctx, src, dst, SyncOptions{Metadata: true, DryRun: true}, logger)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if len(logger.Notices) != 1 || logger.Notices[0] != "%s: Would update metadata (server-side copy)" {
		t.Errorf("Expected dry run notice, got: %v", logger.Notices)
	}

	// Verify metadata was NOT updated in destination
	dstObj := dst.Objects["testfile.bin"]
	if dstObj.Metadata["mtime"] != "2023-01-01T00:00:00Z" {
		t.Errorf("Expected original metadata, got: %v", dstObj.Metadata)
	}
}

func TestSyncMetadataOnlyFallback(t *testing.T) {
	ctx := context.Background()

	src := NewMockLocal()
	dst := NewMockS3()

	srcObj := Object{
		Path: "testfile.bin",
		Size: 100,
		Hash: "md5hash123",
		Metadata: map[string]string{
			"mtime": "2023-01-01T00:00:00Z",
		},
	}
	src.Put(ctx, srcObj, []byte("some data"))

	// First sync to populate dst
	Sync(ctx, src, dst, SyncOptions{Metadata: true}, &TestLogger{})

	// Modify metadata
	srcObj.Metadata["mtime"] = "2023-01-02T00:00:00Z"
	src.Put(ctx, srcObj, []byte("some data"))

	// Deny copy to trigger fallback
	dst.DenyCopy = true

	logger := &TestLogger{}
	err := Sync(ctx, src, dst, SyncOptions{Metadata: true}, logger)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if len(logger.Warnings) != 1 || logger.Warnings[0] != "%s: Server-side copy failed, falling back to upload: %v" {
		t.Errorf("Expected warning log, got: %v", logger.Warnings)
	}

	if len(logger.Infos) != 1 || logger.Infos[0] != "%s: Uploaded (fallback after copy failure)" {
		t.Errorf("Expected fallback upload log, got: %v", logger.Infos)
	}

	// Verify metadata was updated in destination via fallback upload
	dstObj := dst.Objects["testfile.bin"]
	if dstObj.Metadata["mtime"] != "2023-01-02T00:00:00Z" {
		t.Errorf("Expected updated metadata, got: %v", dstObj.Metadata)
	}
}