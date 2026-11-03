package paytochkabank

import (
	"net/url"
	"strings"
	"unicode"
)

func validateRequired(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return &ValidationError{Field: field, Message: "must not be empty"}
	}
	return nil
}

func validatePositiveAmount(field string, amount Money) error {
	if amount <= 0 {
		return &ValidationError{Field: field, Message: "must be greater than zero"}
	}
	return nil
}

func validatePathID(field, value string) error {
	if err := validateRequired(field, value); err != nil {
		return err
	}
	if value == "." || value == ".." || strings.ContainsAny(value, "/\\?#%") || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return &ValidationError{Field: field, Message: "must be a single path identifier"}
	}
	return nil
}

func validateOptionalURL(field, value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return &ValidationError{Field: field, Message: "must be an absolute HTTP or HTTPS URL without user information"}
	}
	return nil
}
