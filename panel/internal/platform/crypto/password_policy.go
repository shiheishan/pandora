package crypto

import (
	"errors"
	"unicode"
)

// ValidatePassword enforces Pandora Panel's product password policy: at least
// eight characters with both a letter and a number. Symbols are allowed but
// are not required. The upper bound prevents excessive Argon2 input sizes.
func ValidatePassword(password string) error {
	runes := []rune(password)
	if len(runes) < 8 {
		return errors.New("密码至少需要 8 个字符")
	}
	if len(password) > 256 {
		return errors.New("密码过长")
	}

	var hasLetter, hasDigit bool
	for _, r := range runes {
		hasLetter = hasLetter || unicode.IsLetter(r)
		hasDigit = hasDigit || unicode.IsDigit(r)
	}
	if !hasLetter || !hasDigit {
		return errors.New("密码必须同时包含字母和数字")
	}
	return nil
}
