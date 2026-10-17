# 门户配色候选（2026-10-16）

四套配色都套在同一个 v4 布局上，在 `portal-v5.html` 里可以切换：`?pal=a|b|c|d`，加 `&shot=1` 隐藏切换条。用户看过后觉得四套都可以；10-16 先默认用 D 森林绿（样稿不带 `?pal` 时就是它），其余三套保留。

## 每个变量管什么

| 变量 | 用在哪 |
|---|---|
| `--bg` | 页面底色（上面叠点阵） |
| `--sh` | 阴影、细线、点阵共用的 RGB 三元组 |
| `--ink` / `--text-2` / `--text-3` | 正文 / 次要文字 / 弱文字 |
| `--paper-2` / `--paper-3` | 卡片的渐变底、行悬停 |
| `--tint` / `--tint-line` | 主卡（我的套餐、余额、推荐套餐）的铺底光晕和描边 |
| `--deep` | 角标底、头像字、行图标、手机标签栏选中 |
| `--act` | 链接、导航选中图标（「去支付」「复制」） |
| `--mark` | logo 后面的小圆点 |
| `--btn` / `--btn-border` / `--btn-glow` | 普通主按钮 |
| `--hot` / `--hot-border` | 最重要的那个按钮（添加到 App、推荐套餐的购买） |
| `--bar` | 流量进度条 |

所有配色共用：警示 `#8a5a00` / `#fbf1dc`，危险 `#a3283a` / `#f9e4e6`，「已支付」保持语义绿 `#23603c` / `#e4f1e8`。

## A 朱砂墨

主色是墨色，朱砂只点在 logo 圆点、进度条末端和链接上。和现在的 logo 红点、后台的墨色系统一脉相承。
参考：Wealthsimple（暖白底，近黑的按钮，彩色只出现在图表里），Granola。

```css
--bg: #f3f3f1; --sh: 28, 26, 24;
--ink: #161616; --text-2: #6b6a66; --text-3: #9c9b96;
--paper-2: #f7f7f5; --paper-3: #f1f1ee;
--tint: #ecebe7; --tint-line: #d9d7d1;
--deep: #1b1b1c; --act: #b23a26; --mark: #c9432d;
--btn: linear-gradient(180deg, #303033, #161618); --btn-border: #0b0b0c; --btn-glow: rgba(10, 10, 12, 0.85);
--hot: linear-gradient(180deg, #303033, #161618); --hot-border: #0b0b0c;
--bar: linear-gradient(90deg, #1b1b1c 0%, #3a3a3c 55%, #c9432d 100%);
```

试过把 hot 按钮做成墨色渐变到砖红，看着发脏，已改成纯墨色。

## B 深海珊瑚

主色是深海军蓝，珊瑚红只给最重要的那个动作。在三套里层级最清楚。珊瑚和 logo 红点是同一个色族。
参考：Monzo（珊瑚卡片配深海军蓝）。

```css
--bg: #f0f3f6; --sh: 14, 32, 52;
--ink: #0f1c2b; --text-2: #5d6a78; --text-3: #93a0ad;
--paper-2: #f6f8fa; --paper-3: #eef2f6;
--tint: #e0eaf3; --tint-line: #c0d2e2;
--deep: #112231; --act: #cf4630; --mark: #ff6a4d;
--btn: linear-gradient(180deg, #213a52, #112231); --btn-border: #08131d; --btn-glow: rgba(8, 22, 36, 0.85);
--hot: linear-gradient(180deg, #f6694f, #dc4a31); --hot-border: #b9381f;
--bar: linear-gradient(90deg, #112231 0%, #2a4863 50%, #ff6a4d 100%);
```

## C 雾蓝

冷白底，长春花蓝做主色，淡紫铺底。最柔和，但和常见的 SaaS 蓝紫比较接近。
参考：Mercury（冷灰白底，长春花蓝按钮，淡紫面积图）。

```css
--bg: #f4f5f8; --sh: 34, 38, 80;
--ink: #161925; --text-2: #656b7b; --text-3: #9aa0ae;
--paper-2: #f8f9fc; --paper-3: #f1f2f8;
--tint: #e7e8fb; --tint-line: #c9cdf4;
--deep: #2c3170; --act: #4b55d1; --mark: #5e6af0;
--btn: linear-gradient(180deg, #6470f2, #4a55d6); --btn-border: #3d47b9; --btn-glow: rgba(60, 70, 190, 0.6);
--hot: linear-gradient(90deg, #3f49c4 0%, #5763e6 55%, #7e8bfa 100%); --hot-border: #363fae;
--bar: linear-gradient(90deg, #4a55d6 0%, #7380f5 60%, #b3bbff 100%);
```

## D 森林绿

v4 原样的绿，取自用户给的 Laper 定价页参考图。用户觉得它像硬抄、和我们的身份不搭，这里留档对比。

```css
--bg: #f4f5f2; --sh: 20, 40, 30;
--ink: #141715; --text-2: #6a706b; --text-3: #969b96;
--paper-2: #f6f8f6; --paper-3: #f3f6f4;
--tint: #dfe8e1; --tint-line: #b4c8ba;
--deep: #1f3d2b; --act: #2c5a3f; --mark: #4cbb72;
--btn: linear-gradient(180deg, #2c5a3f, #1f3d2b); --btn-border: #10251a; --btn-glow: rgba(16, 37, 26, 0.9);
--hot: linear-gradient(90deg, #1f3d2b 0%, #2c5a3f 45%, #4cbb72 100%); --hot-border: #10251a;
--bar: linear-gradient(90deg, #1f3d2b, #4cbb72);
```

## 落到正式代码时

- 门户的外观主题会在 `<html>` 上按 COLOR_TOKENS 白名单写颜色，所以换色要走这条路，不能只改样式表：COLOR_TOKENS 在 `tokens.css`、`design-tokens.ts`、Go `DesignTokenKeys`、迁移 `00029_appearance.sql` 的种子四处同步。
- 每套还要补暗色。
- 样稿里的 Courier Prime 与 Noto Serif SC 走的是 Google Fonts，正式实现要自托管（CSP `font-src 'self'`）。
