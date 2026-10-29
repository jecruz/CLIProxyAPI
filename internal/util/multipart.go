package util

import (
	"fmt"
	"strings"
)

var multipartQuoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "%0D", "\n", "%0A")

// FileContentDisposition returns a multipart form-data Content-Disposition value.
func FileContentDisposition(fieldname, filename string) string {
	return fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
		multipartQuoteEscaper.Replace(fieldname), multipartQuoteEscaper.Replace(filename))
}
