package main

import "testing"

func TestGoIdentifier(t *testing.T) {
	cases := map[string]string{
		"FAILURE_REASON_STORE_UNAVAILABLE":         "ErrStoreUnavailable",
		"FAILURE_REASON_SESSION_SUBJECT_NOT_FOUND": "ErrSessionSubjectNotFound",
		"FAILURE_REASON_INTERNAL_UNEXPECTED":       "ErrInternalUnexpected",
		"FAILURE_REASON_PERMISSION_CHECK_FAILED":   "ErrPermissionCheckFailed",
	}
	for protoName, want := range cases {
		if got := goIdentifier(protoName); got != want {
			t.Errorf("goIdentifier(%q) = %q, want %q", protoName, got, want)
		}
	}
}

func TestParseConnectCodeAcceptsOnlyStandardCodes(t *testing.T) {
	for _, name := range []string{"not_found", "permission_denied", "internal", "unauthenticated"} {
		if _, ok := parseConnectCode(name); !ok {
			t.Errorf("标准码 %q 未被接受", name)
		}
	}
	for _, name := range []string{"", "Not_Found", "not found", "teapot", "unknown_code"} {
		if _, ok := parseConnectCode(name); ok {
			t.Errorf("非法码 %q 被接受了", name)
		}
	}
}

func TestCheckSegmentBoundaries(t *testing.T) {
	cases := []struct {
		number int32
		ok     bool
	}{
		{999, false},
		{1000, true},
		{1099, true},
		{1100, true},
		{1199, true},
		{1200, true},
		{1899, false},
		{1900, true},
		{1999, true},
		{2000, false},
	}
	for _, tc := range cases {
		err := checkSegment(tc.number, "FAILURE_REASON_TEST")
		if (err == nil) != tc.ok {
			t.Errorf("checkSegment(%d) err = %v, want ok=%v", tc.number, err, tc.ok)
		}
	}
}
