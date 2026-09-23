/**
 * [INPUT]: 依赖 core/auth 的 useAuth、core/api 的 failure、core/runtime 的 runtime 与 legacyEntry、core/appearance 的 useBranding 与 PortalSlot，依赖 antd 表单组件
 * [OUTPUT]: 对外提供 Login 组件
 * [POS]: app 壳层的登录页，被 Gateway 与 Frame 在无会话时渲染；按域切换文案，底部链接回原版手写单页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from "react";
import { Alert, Button, Card, Form, Input, Typography } from "antd";
import { LockOutlined, MailOutlined } from "@ant-design/icons";
import { useAuth } from "../core/auth";
import { failure } from "../core/api";
import { legacyEntry, runtime } from "../core/runtime";
import { useBranding, PortalSlot } from "../core/appearance";

export function Login() {
  const branding = useBranding();
  const { login, sessionError } = useAuth();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (values: { email: string; password: string }) => {
    setBusy(true);
    setError("");
    try {
      await login(values.email.trim(), values.password);
    } catch (error) {
      setError(failure(error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="login-page">
      <div className="login-story">
        <div className="brand">
          <span className="brand-mark">P</span>
          <b>{branding.name}</b>
        </div>
        <Typography.Title>
          连接你的世界。
          <br />
          让管理更从容。
        </Typography.Title>
        <p>
          清楚的状态，顺畅的操作。
          <br />
          每一次连接，都在掌握之中。
        </p>
        <div className="login-orbit" aria-hidden="true" />
      </div>
      <div className="login-form-area">
        <div className="login-content-stack">
        <PortalSlot name="portal.login.notice" />
        <Card className="login-card" variant="borderless">
          {branding.tagline && <Typography.Paragraph>{branding.tagline}</Typography.Paragraph>}
          <Typography.Text type="secondary">
            {branding.name} {runtime.domain === "admin" ? "CONSOLE" : "ACCOUNT"}
          </Typography.Text>
          <Typography.Title level={2}>
            {runtime.domain === "admin" ? "欢迎回到控制台" : "欢迎回来"}
          </Typography.Title>
          <Typography.Paragraph type="secondary">
            登录后继续你的工作
          </Typography.Paragraph>
          {sessionError && (
            <Alert
              type="warning"
              className="mb"
              title="暂时无法恢复会话"
              description={sessionError}
              action={<Button onClick={() => location.reload()}>重试</Button>}
            />
          )}
          {error && (
            <Alert type="error" showIcon title={error} className="mb" />
          )}
          <Form
            layout="vertical"
            onFinish={(values) => void submit(values)}
            requiredMark={false}
          >
            <Form.Item
              label="邮箱"
              name="email"
              rules={[
                { required: true, type: "email", message: "请输入有效邮箱" },
              ]}
            >
              <Input
                prefix={<MailOutlined />}
                autoComplete="username"
                size="large"
                placeholder="name@example.com"
              />
            </Form.Item>
            <Form.Item
              label="密码"
              name="password"
              rules={[{ required: true, message: "请输入密码" }]}
            >
              <Input.Password
                prefix={<LockOutlined />}
                autoComplete="current-password"
                size="large"
              />
            </Form.Item>
            <Button
              htmlType="submit"
              type="primary"
              size="large"
              loading={busy}
              block
            >
              登录
            </Button>
          </Form>
          <p className="login-foot">
            {runtime.domain === "admin"
              ? "管理权限由服务器验证"
              : "忘记密码请联系服务方协助处理"}
          </p>
          <a className="login-backlink" href={legacyEntry}>
            返回原版入口
          </a>
        </Card>
        <PortalSlot name="portal.footer" />
        </div>
      </div>
    </main>
  );
}
