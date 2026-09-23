import { principalSubject } from "../../core/data";
import { createdResourceSchema } from "../../core/data";
import { useEffect, useRef, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  Select,
  Space,
  Typography,
} from "antd";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { useObjectDraft } from "../../core/formDraft";
import {
  dateText,
  enc,
  idOf,
  integer,
  record,
  text,
  type Row,
} from "../../core/data";
import { failure } from "../../core/api";
import {
  BackLink,
  EntityLink,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  useData,
} from "../../components/common";

export function ContentPages() {
  const { can } = useAuth();
  return (
    <ResourcePage
      title="知识库与页面"
      resource="content"
      path="v1/content-pages?limit=200"
      listKey="pages"
      searchable
      extra={
        can("ops.content.write") && (
          <Link to="/content/new">
            <Button type="primary">新建文章</Button>
          </Link>
        )
      }
      columns={[
        {
          title: "标题",
          render: (_, row) => (
            <EntityLink row={row} path="/content" label="title" />
          ),
        },
        { title: "地址别名", dataIndex: "slug" },
        { title: "分类", dataIndex: "category" },
        {
          title: "状态",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        { title: "版本", dataIndex: "version" },
        { title: "更新时间", dataIndex: "updated_at", render: dateText },
      ]}
    />
  );
}
export function ContentEditor() {
  const { id } = useParams();
  const creating = !id || id === "new";
  const { api, principal } = useAuth();
  const query = useData("content", `v1/content-pages/${enc(id)}`, !creating);
  const page = record(query.data?.page);
  const draft = useObjectDraft(
    principalSubject(principal),
    `content:${id || "new"}`,
  );
  const [form] = Form.useForm<Row>();
  const loaded = useRef(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const navigate = useNavigate();
  const dialog = useDialog();
  const { message } = App.useApp();
  const attempt = useRef(
    new Operation("content-publish", principalSubject(principal)),
  );
  useEffect(() => {
    if (loaded.current || (!creating && !query.data)) return;
    form.setFieldsValue({
      kind: "kb_article",
      locale: "zh-CN",
      visibility: "authenticated",
      target_platforms: ["web"],
      ...page,
      ...draft.value,
    });
    loaded.current = true;
  }, [creating, query.data, form, page, draft.value]);
  const submit = async (values: Row) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const payload: Row = {};
      for (const key of [
        "slug",
        "kind",
        "category",
        "title",
        "summary",
        "body",
        "locale",
        "visibility",
        "target_platforms",
        "min_client_version",
        "max_client_version",
      ])
        payload[key] = values[key];
      const result = await attempt.current.send(
        api,
        "v1/content-pages",
        {
          ...payload,
          status: values.status || "draft",
          target_plan_ids: page.target_plan_ids || [],
          review_due_at: page.review_due_at || null,
          expected_latest_version: integer(
            page.latest_version,
            integer(page.version),
          ),
        },
        {},
        createdResourceSchema("page"),
      );
      draft.clear();
      const saved = record(result.page);
      message.success(
        saved.status === "published" ? "新版本已发布" : "文章草稿已保存",
      );
      navigate("/content");
    } catch (value) {
      setError(failure(value).message);
    } finally {
      setBusy(false);
    }
  };
  const archive = () => {
    const operation = new Operation(
      "content-archive",
      principalSubject(principal),
    );
    return dialog({
      title: "归档文章",
      danger: true,
      description: "此版本停止对用户展示。历史版本会保留。",
      onSubmit: async () => {
        await operation.send(
          api,
          `v1/content-pages/${enc(idOf(page))}/archive`,
          { expected_version: page.version },
        );
        navigate("/content");
      },
    });
  };
  const editor = (
    <Form
      form={form}
      layout="vertical"
      disabled={busy}
      onValuesChange={(_, values) => draft.setValue(values)}
      onFinish={(values) => void submit(values)}
    >
      <div className="split-detail">
        <Card title="文章内容">
          <Form.Item name="title" label="标题" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="summary" label="摘要">
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item
            name="body"
            label="正文"
            rules={[{ required: true }]}
            extra="以纯文本或Markdown保存，用户页面不执行文章中的HTML。"
          >
            <Input.TextArea rows={18} />
          </Form.Item>
          <Typography.Text type="secondary">
            {draft.persisted
              ? "文章草稿保存在本标签页"
              : "草稿存储不可用，请勿关闭"}
          </Typography.Text>
        </Card>
        <Card title="发布设置">
          <Form.Item
            name="slug"
            label="地址别名"
            rules={[
              { required: true },
              {
                pattern: /^[a-z0-9]+(?:-[a-z0-9]+)*$/,
                message: "使用小写字母、数字和单个短横线",
              },
            ]}
          >
            <Input disabled={!creating} />
          </Form.Item>
          <Form.Item name="kind" label="类型">
            <Select
              options={[
                { value: "kb_article", label: "知识库文章" },
                { value: "tutorial", label: "使用教程" },
                { value: "page", label: "自定义页面" },
                { value: "legal", label: "条款" },
              ]}
            />
          </Form.Item>
          <Form.Item name="category" label="分类">
            <Input />
          </Form.Item>
          <Form.Item name="locale" label="语言">
            <Select
              options={[
                { value: "zh-CN", label: "简体中文" },
                { value: "en", label: "English" },
              ]}
            />
          </Form.Item>
          <Form.Item name="visibility" label="可见性">
            <Select
              options={[
                { value: "authenticated", label: "登录用户" },
                { value: "public", label: "公开" },
              ]}
            />
          </Form.Item>
          <Form.Item name="target_platforms" label="适用平台">
            <Select
              mode="multiple"
              options={[
                "web",
                "windows",
                "macos",
                "linux",
                "android",
                "ios",
              ].map((value) => ({ label: value, value }))}
            />
          </Form.Item>
          <Form.Item name="min_client_version" label="最低客户端版本">
            <Input />
          </Form.Item>
          <Form.Item name="max_client_version" label="最高客户端版本">
            <Input />
          </Form.Item>
          <Space wrap>
            <Button
              htmlType="submit"
              loading={busy}
              onClick={() => form.setFieldValue("status", "draft")}
            >
              保存草稿
            </Button>
            <Button
              type="primary"
              htmlType="submit"
              loading={busy}
              onClick={() => form.setFieldValue("status", "published")}
            >
              发布新版本
            </Button>
          </Space>
        </Card>
      </div>
      <Form.Item name="status" hidden>
        <Input />
      </Form.Item>
    </Form>
  );
  return (
    <>
      <PageHeader
        title={creating ? "新建知识库文章" : text(page.title, "编辑文章")}
        extra={
          <Space>
            <BackLink to="/content" />
            {!creating && page.status === "published" && (
              <Button danger onClick={() => void archive()}>
                归档
              </Button>
            )}
          </Space>
        }
      />
      {error && <Alert className="mb" type="error" showIcon title={error} />}
      {creating ? editor : <QueryPanel query={query}>{editor}</QueryPanel>}
    </>
  );
}
