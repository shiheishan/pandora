package render

// 可套自定义模板的格式；模板原文由 subscription 模块存库，拉取时经 Request.Template 传入，占位替换在 fillTemplate。

// TemplateFormats 是有自定义模板的格式（库里 subscription_templates 可能有行）。
var TemplateFormats = []Format{
	FormatClash,
	FormatClashPremium,
	FormatStash,
	FormatSingbox,
	FormatSurfboard,
	FormatLoon,
}

// Templatable 判断 f 能不能套库里的自定义模板。
func Templatable(f Format) bool {
	for _, candidate := range TemplateFormats {
		if f == candidate {
			return true
		}
	}
	return false
}
