package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// MaxBodyBytes 是 JSON 请求体的上限。
const MaxBodyBytes = 1 << 20

var tagNameOnce sync.Once

// useJSONNames 让校验错误里的字段名取 json 标签（查询参数取 form 标签），与前端看到的名字一致。
func useJSONNames() {
	tagNameOnce.Do(func() {
		v, ok := binding.Validator.Engine().(*validator.Validate)
		if !ok {
			return
		}
		v.RegisterTagNameFunc(func(f reflect.StructField) string {
			for _, tag := range []string{"json", "form"} {
				name := strings.Split(f.Tag.Get(tag), ",")[0]
				if name == "-" {
					return ""
				}
				if name != "" {
					return name
				}
			}
			return f.Name
		})
	})
}

// BindJSON 绑定并校验 JSON 请求体：上限 1 MiB、拒绝未知字段、只允许一个 JSON 值。
func BindJSON(c *gin.Context, dst any) error {
	useJSONNames()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errs.Newf(errs.BadRequest, "请求体不是合法的 JSON")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errs.Newf(errs.BadRequest, "请求体不是合法的 JSON")
	}
	if err := binding.Validator.ValidateStruct(dst); err != nil {
		return validationToErrs(err)
	}
	return nil
}

// BindQuery 绑定并校验查询参数。
func BindQuery(c *gin.Context, dst any) error {
	useJSONNames()
	if err := c.ShouldBindQuery(dst); err != nil {
		return validationToErrs(err)
	}
	return nil
}

func validationToErrs(err error) error {
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return errs.New(errs.BadRequest)
	}
	fields := make(map[string]string, len(verrs))
	for _, fe := range verrs {
		fields[fe.Field()] = validationMessage(fe.Tag())
	}
	return errs.Invalid(fields)
}

func validationMessage(tag string) string {
	switch tag {
	case "required":
		return "必填"
	case "max", "lte":
		return "超出上限"
	case "min", "gte":
		return "低于下限"
	case "len":
		return "长度不对"
	case "email":
		return "邮箱格式不正确"
	case "oneof":
		return "取值不在允许范围内"
	default:
		return "格式不正确"
	}
}
