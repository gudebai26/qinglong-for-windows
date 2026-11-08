package httpapi

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

// errFileTooLarge mirrors multer's LIMIT_FILE_SIZE, which the panel answers
// with a 500 because no error handler maps it.
var errFileTooLarge = errors.New("File too large")

// uploadedFile is the single file multer would have stored for a route.
type uploadedFile struct {
	// Path is the temporary file on disk (multer.diskStorage).
	Path string
	// Data holds the bytes for memoryStorage (env uploads).
	Data []byte
	// Name is the original client file name.
	Name string
	Size int64
}

// parseMultipart reads a multipart/form-data body the way multer.single() does:
// text fields become strings, the first file of fieldName is captured, and any
// other file part is drained. The body size limit is enforced by the bodyLimit
// middleware; this only guards the per-file limit of memory storage.
func parseMultipart(r *http.Request, fieldName, tmpDir string, inMemory bool, maxBytes int64) (map[string]any, *uploadedFile, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, err
	}
	fields := map[string]any{}
	var uploaded *uploadedFile

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		name := part.FormName()
		if name == "" {
			part.Close()
			continue
		}
		if part.FileName() == "" {
			value, err := io.ReadAll(part)
			part.Close()
			if err != nil {
				return nil, nil, err
			}
			fields[name] = string(value)
			continue
		}
		if name != fieldName || uploaded != nil {
			part.Close()
			continue
		}
		fileName := part.FileName()
		file, err := storeUpload(part, tmpDir, inMemory, maxBytes)
		part.Close()
		if err != nil {
			return nil, nil, err
		}
		file.Name = fileName
		uploaded = file
	}
	return fields, uploaded, nil
}

func storeUpload(part *multipart.Part, tmpDir string, inMemory bool, maxBytes int64) (*uploadedFile, error) {
	if inMemory {
		limited := &io.LimitedReader{R: part, N: maxBytes + 1}
		data, err := io.ReadAll(limited)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxBytes {
			return nil, errFileTooLarge
		}
		return &uploadedFile{Data: data, Size: int64(len(data))}, nil
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, err
	}
	target := filepath.Join(tmpDir, uuidV4())
	out, err := os.Create(target)
	if err != nil {
		return nil, err
	}
	size, err := io.Copy(out, part)
	if err != nil {
		out.Close()
		os.Remove(target)
		return nil, err
	}
	if err := out.Close(); err != nil {
		os.Remove(target)
		return nil, err
	}
	return &uploadedFile{Path: target, Size: size}, nil
}

// isMultipart reports whether the request body is multipart/form-data.
func isMultipart(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mediaType == "multipart/form-data"
}
