package util

import "testing"

func TestFileContentDispositionEscapesFormDataParameters(t *testing.T) {
	got := FileContentDisposition(`field\\name`, "file\"name\r\n.txt")
	want := `form-data; name="field\\\\name"; filename="file\"name%0D%0A.txt"`
	if got != want {
		t.Fatalf("FileContentDisposition() = %q, want %q", got, want)
	}
}
