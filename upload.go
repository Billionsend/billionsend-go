package billionsend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"strings"
)

// File is a file to upload.
type File struct {
	// Content is the file's bytes, for example from os.ReadFile.
	Content []byte
	// Name is the file name sent with the upload. Empty: logo.png, logo.jpg or
	// logo.webp (by what the bytes are) for a logo, contacts.csv for a CSV.
	Name string
	// ContentType is the part's content type. Empty: judged from the bytes for
	// a logo, text/csv for a CSV.
	ContentType string
}

// UploadLogoForm is the multipart/form-data body of brand.logo.upload and
// domains.unsubscribePage.logo.upload.
type UploadLogoForm struct {
	// The image: PNG, JPEG or WebP (judged from its bytes, not its name or
	// type), at most 2 MiB. SVG is not accepted.
	File File
}

// CreateContactImportForm is the multipart/form-data body of
// contacts.imports.create. ColumnMap, Segments and Topics are sent
// JSON-encoded.
type CreateContactImportForm struct {
	// The CSV file: a header row, then one contact per row. At most 20 MiB.
	File File
	// Which CSV column feeds which field. A field left out is found by its usual
	// header (email, first_name, last_name, unsubscribed).
	ColumnMap *ContactImportColumnMap
	// What to do with an address the team already has: "upsert" (the default)
	// updates it from the file, "skip" leaves it alone.
	OnConflict *string
	// Segments every imported contact joins.
	Segments []ContactImportSegment
	// Topic subscriptions to set for every imported contact.
	Topics []ContactImportTopic
}

// ContactImportColumnMap names the CSV columns an import reads.
type ContactImportColumnMap struct {
	Email     *string `json:"email,omitempty"`
	FirstName *string `json:"first_name,omitempty"`
	LastName  *string `json:"last_name,omitempty"`
	// The column that says a contact is unsubscribed (true, 1, yes). It only
	// ever opts a contact out.
	Unsubscribed *string `json:"unsubscribed,omitempty"`
	// Contact properties to fill, by property key.
	Properties map[string]ContactImportPropertyColumn `json:"properties,omitempty"`
}

// ContactImportPropertyColumn is the CSV column that fills one contact property.
type ContactImportPropertyColumn struct {
	Column string `json:"column"`
	// One of: "string", "number", "boolean".
	Type *string `json:"type,omitempty"`
}

// ContactImportSegment is a segment imported contacts join.
type ContactImportSegment struct {
	ID string `json:"id"`
}

// ContactImportTopic is a topic subscription imported contacts get.
type ContactImportTopic struct {
	ID string `json:"id"`
	// One of: "opt_in", "opt_out".
	Subscription string `json:"subscription"`
}

// multipartBody is a request body sent as multipart/form-data.
type multipartBody interface {
	encodeMultipart() (body []byte, contentType string, err error)
}

func (f *UploadLogoForm) encodeMultipart() ([]byte, string, error) {
	file := f.File
	extension, sniffed := sniffImage(file.Content)
	if file.Name == "" {
		file.Name = "logo." + extension
	}
	if file.ContentType == "" {
		file.ContentType = sniffed
	}
	return encodeForm(file, nil)
}

func (f *CreateContactImportForm) encodeMultipart() ([]byte, string, error) {
	file := f.File
	if file.Name == "" {
		file.Name = "contacts.csv"
	}
	if file.ContentType == "" {
		file.ContentType = "text/csv"
	}
	var fields [][2]string
	add := func(name string, present bool, value any) error {
		if !present {
			return nil
		}
		raw, err := json.Marshal(value)
		fields = append(fields, [2]string{name, string(raw)})
		return err
	}
	if err := add("column_map", f.ColumnMap != nil, f.ColumnMap); err != nil {
		return nil, "", err
	}
	if f.OnConflict != nil {
		fields = append(fields, [2]string{"on_conflict", *f.OnConflict})
	}
	if err := add("segments", f.Segments != nil, f.Segments); err != nil {
		return nil, "", err
	}
	if err := add("topics", f.Topics != nil, f.Topics); err != nil {
		return nil, "", err
	}
	return encodeForm(file, fields)
}

// sniffImage tells the three accepted logo formats apart by their magic
// numbers; anything else is left for the API to refuse.
func sniffImage(b []byte) (extension, contentType string) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png", "image/png"
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return "jpg", "image/jpeg"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "webp", "image/webp"
	}
	return "png", "application/octet-stream"
}

var quoteFilename = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "%0D", "\n", "%0A")

// encodeForm writes the file as the part "file", then the text fields.
func encodeForm(file File, fields [][2]string) ([]byte, string, error) {
	if strings.ContainsAny(file.ContentType, "\r\n") {
		return nil, "", fmt.Errorf("the file's content type has a line break")
	}
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+quoteFilename.Replace(file.Name)+`"`)
	h.Set("Content-Type", file.ContentType)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(file.Content); err != nil {
		return nil, "", err
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return b.Bytes(), w.FormDataContentType(), nil
}
