package service

// Xboard 补齐用例共用的假端口（普通单测与 PG 用例都能用）。

import (
	"context"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

// fakeSite 是固定时区的站点设置。
type fakeSite struct{ tz string }

func (f fakeSite) Site(context.Context) (settingssvc.Site, error) {
	return settingssvc.Site{Name: "测试站", Timezone: f.tz, Currency: "CNY"}, nil
}

// fakeSealer 是不加密的封存器：密文 = "sealed:" + AAD + ":" + 明文，Open 核对 AAD，供测试断言 AAD 用对了。
type fakeSealer struct{}

func (fakeSealer) Seal(plain, aad []byte) ([]byte, error) {
	out := append([]byte("sealed:"), aad...)
	out = append(out, ':')
	return append(out, plain...), nil
}

func (fakeSealer) Open(sealed, aad []byte) ([]byte, error) {
	prefix := append(append([]byte("sealed:"), aad...), ':')
	if len(sealed) < len(prefix) || string(sealed[:len(prefix)]) != string(prefix) {
		return nil, errFakeSealerAAD
	}
	return append([]byte(nil), sealed[len(prefix):]...), nil
}

var errFakeSealerAAD = errFake("AAD 不符")

type errFake string

func (e errFake) Error() string { return string(e) }
