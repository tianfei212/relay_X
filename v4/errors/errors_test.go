package errors

import (
	"errors"
	"testing"
)

// TestCodeAndWrap 验证 Wrap/Code/errors.Is 的基础行为。
func TestCodeAndWrap(t *testing.T) {
	base := errors.New("x")
	e := Wrap(CodeConflict, "conflict", base)
	if Code(e) != CodeConflict {
		t.Fatalf("code=%d", Code(e))
	}
	if !errors.Is(e, base) {
		t.Fatalf("unwrap failed")
	}
}

// TestWithMessageAndCodeFallback 验证 WithMessage 及默认错误码回退。
func TestWithMessageAndCodeFallback(t *testing.T) {
	base := errors.New("x")
	w := WithMessage(base, "ctx")
	if w == nil {
		t.Fatalf("expected error")
	}
	if Code(base) != 500 {
		t.Fatalf("expected default code")
	}
	if Code(nil) != 0 {
		t.Fatalf("expected code 0 for nil")
	}
}

// TestNewAndWithMessageOnCodeError 验证 CodeError 的 New/WithMessage/Wrap 组合行为。
func TestNewAndWithMessageOnCodeError(t *testing.T) {
	ce := New(CodeBadRequest, "bad")
	if Code(ce) != CodeBadRequest {
		t.Fatalf("code=%d", Code(ce))
	}
	if ce.Error() == "" {
		t.Fatalf("expected message")
	}
	if ce.Unwrap() != nil {
		t.Fatalf("expected nil unwrap")
	}
	w := WithMessage(ce, "ctx")
	if Code(w) != CodeBadRequest {
		t.Fatalf("code=%d", Code(w))
	}
	w2 := Wrap(CodeBadRequest, "ctx", nil)
	if Code(w2) != CodeBadRequest {
		t.Fatalf("code=%d", Code(w2))
	}
}
