package main

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// Object represents a file/object in storage.
type Object struct {
	Path     string
	Size     int64
	Hash     string
	Metadata map[string]string
}

// CopyOptions defines options for the Copy operation.
type CopyOptions struct {
	MetadataDirective string            // "REPLACE" or "COPY"
	NewMetadata       map[string]string
}

// Storage defines the interface for our storage backends.
type Storage interface {
	List(ctx context.Context) ([]Object, error)
	Put(ctx context.Context, obj Object, data []byte) error
	Copy(ctx context.Context, src Object, dstPath string, options CopyOptions) error
	Get(ctx context.Context, path string) ([]byte, error)
}

// MockS3 implements Storage to simulate an S3 backend.
type MockS3 struct {
	Objects  map[string]Object
	Data     map[string][]byte
	DenyCopy bool
}

func NewMockS3() *MockS3 {
	return &MockS3{
		Objects: make(map[string]Object),
		Data:    make(map[string][]byte),
	}
}

func (m *MockS3) List(ctx context.Context) ([]Object, error) {
	var list []Object
	for _, obj := range m.Objects {
		list = append(list, obj)
	}
	return list, nil
}

func (m *MockS3) Put(ctx context.Context, obj Object, data []byte) error {
	m.Objects[obj.Path] = obj
	m.Data[obj.Path] = data
	return nil
}

func (m *MockS3) Copy(ctx context.Context, src Object, dstPath string, options CopyOptions) error {
	if m.DenyCopy {
		return errors.New("permission denied: CopyObject")
	}
	if src.Path == dstPath {
		obj, exists := m.Objects[src.Path]
		if !exists {
			return errors.New("source object not found")
		}
		if options.MetadataDirective == "REPLACE" {
			obj.Metadata = make(map[string]string)
			for k, v := range options.NewMetadata {
				obj.Metadata[k] = v
			}
			m.Objects[src.Path] = obj
		}
		return nil
	}
	return errors.New("cross-object copy not implemented in mock")
}

func (m *MockS3) Get(ctx context.Context, path string) ([]byte, error) {
	data, exists := m.Data[path]
	if !exists {
		return nil, errors.New("not found")
	}
	return data, nil
}

// MockLocal implements Storage to simulate a local filesystem.
type MockLocal struct {
	Objects map[string]Object
	Data    map[string][]byte
}

func NewMockLocal() *MockLocal {
	return &MockLocal{
		Objects: make(map[string]Object),
		Data:    make(map[string][]byte),
	}
}

func (m *MockLocal) List(ctx context.Context) ([]Object, error) {
	var list []Object
	for _, obj := range m.Objects {
		list = append(list, obj)
	}
	return list, nil
}

func (m *MockLocal) Put(ctx context.Context, obj Object, data []byte) error {
	m.Objects[obj.Path] = obj
	m.Data[obj.Path] = data
	return nil
}

func (m *MockLocal) Copy(ctx context.Context, src Object, dstPath string, options CopyOptions) error {
	return errors.New("copy not supported on local filesystem")
}

func (m *MockLocal) Get(ctx context.Context, path string) ([]byte, error) {
	data, exists := m.Data[path]
	if !exists {
		return nil, errors.New("not found")
	}
	return data, nil
}

// SyncOptions defines options for the sync operation.
type SyncOptions struct {
	Metadata bool
	DryRun   bool
	Verbose  bool
}

// Logger is a simple logger interface.
type Logger interface {
	Infof(format string, v ...interface{})
	Warningf(format string, v ...interface{})
	Noticef(format string, v ...interface{})
}

type StdLogger struct{}

func (StdLogger) Infof(format string, v ...interface{})    { log.Printf("INFO: "+format, v...) }
func (StdLogger) Warningf(format string, v ...interface{})  { log.Printf("WARNING: "+format, v...) }
func (StdLogger) Noticef(format string, v ...interface{})   { log.Printf("NOTICE: "+format, v...) }

// MetadataEqual compares two metadata maps.
func MetadataEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// Sync performs the sync operation from src to dst.
func Sync(ctx context.Context, src Storage, dst Storage, opts SyncOptions, logger Logger) error {
	srcList, err := src.List(ctx)
	if err != nil {
		return err
	}

	dstList, err := dst.List(ctx)
	if err != nil {
		return err
	}

	dstMap := make(map[string]Object)
	for _, obj := range dstList {
		dstMap[obj.Path] = obj
	}

	for _, srcObj := range srcList {
		dstObj, exists := dstMap[srcObj.Path]
		if !exists {
			// Upload new file
			if opts.DryRun {
				logger.Noticef("%s: Would upload (new file)", srcObj.Path)
				continue
			}
			data, err := src.Get(ctx, srcObj.Path)
			if err != nil {
				return err
			}
			err = dst.Put(ctx, srcObj, data)
			if err != nil {
				return err
			}
			logger.Infof("%s: Uploaded (new file)", srcObj.Path)
			continue
		}

		// Check if content differs
		contentDiffers := srcObj.Size != dstObj.Size || srcObj.Hash != dstObj.Hash

		if contentDiffers {
			if opts.DryRun {
				logger.Noticef("%s: Would upload (content changed)", srcObj.Path)
				continue
			}
			data, err := src.Get(ctx, srcObj.Path)
			if err != nil {
				return err
			}
			err = dst.Put(ctx, srcObj, data)
			if err != nil {
				return err
			}
			logger.Infof("%s: Uploaded (content changed)", srcObj.Path)
			continue
		}

		// Content is identical, check metadata if enabled
		if opts.Metadata && !MetadataEqual(srcObj.Metadata, dstObj.Metadata) {
			if opts.DryRun {
				logger.Noticef("%s: Would update metadata (server-side copy)", srcObj.Path)
				continue
			}

			// Attempt server-side copy-to-self
			copyOpts := CopyOptions{
				MetadataDirective: "REPLACE",
				NewMetadata:       srcObj.Metadata,
			}
			err := dst.Copy(ctx, dstObj, dstObj.Path, copyOpts)
			if err == nil {
				logger.Infof("%s: Updated metadata (server-side copy)", srcObj.Path)
			} else {
				logger.Warningf("%s: Server-side copy failed, falling back to upload: %v", srcObj.Path, err)
				// Fallback to standard upload
				data, errGet := src.Get(ctx, srcObj.Path)
				if errGet != nil {
					return errGet
				}
				errPut := dst.Put(ctx, srcObj, data)
				if errPut != nil {
					return errPut
				}
				logger.Infof("%s: Uploaded (fallback after copy failure)", srcObj.Path)
			}
		}
	}

	return nil
}

func main() {
	fmt.Println("Hello, Bounty Hunter!")
}