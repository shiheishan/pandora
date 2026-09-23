-- 00065: 礼品卡引用必须绑定同一租户，避免仅凭全局 UUID 跨租户关联。

-- +goose Up
ALTER TABLE gift_card_templates
  ADD CONSTRAINT gift_card_templates_tenant_id_id_key UNIQUE (tenant_id, id);
ALTER TABLE gift_card_codes
  ADD CONSTRAINT gift_card_codes_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE gift_card_codes
  DROP CONSTRAINT gift_card_codes_template_id_fkey,
  ADD CONSTRAINT gift_card_codes_template_fk
    FOREIGN KEY (tenant_id, template_id)
    REFERENCES gift_card_templates (tenant_id, id) ON DELETE RESTRICT;

ALTER TABLE gift_card_redemptions
  DROP CONSTRAINT gift_card_redemptions_code_id_fkey,
  DROP CONSTRAINT gift_card_redemptions_template_id_fkey,
  DROP CONSTRAINT gift_card_redemptions_user_id_fkey,
  ADD CONSTRAINT gift_card_redemptions_code_fk
    FOREIGN KEY (tenant_id, code_id)
    REFERENCES gift_card_codes (tenant_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT gift_card_redemptions_template_fk
    FOREIGN KEY (tenant_id, template_id)
    REFERENCES gift_card_templates (tenant_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT gift_card_redemptions_user_fk
    FOREIGN KEY (tenant_id, user_id)
    REFERENCES users (tenant_id, id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE gift_card_redemptions
  DROP CONSTRAINT gift_card_redemptions_code_fk,
  DROP CONSTRAINT gift_card_redemptions_template_fk,
  DROP CONSTRAINT gift_card_redemptions_user_fk,
  ADD CONSTRAINT gift_card_redemptions_code_id_fkey
    FOREIGN KEY (code_id) REFERENCES gift_card_codes (id) ON DELETE RESTRICT,
  ADD CONSTRAINT gift_card_redemptions_template_id_fkey
    FOREIGN KEY (template_id) REFERENCES gift_card_templates (id) ON DELETE RESTRICT,
  ADD CONSTRAINT gift_card_redemptions_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE RESTRICT;

ALTER TABLE gift_card_codes
  DROP CONSTRAINT gift_card_codes_template_fk,
  ADD CONSTRAINT gift_card_codes_template_id_fkey
    FOREIGN KEY (template_id) REFERENCES gift_card_templates (id) ON DELETE RESTRICT;

ALTER TABLE gift_card_codes
  DROP CONSTRAINT gift_card_codes_tenant_id_id_key;
ALTER TABLE gift_card_templates
  DROP CONSTRAINT gift_card_templates_tenant_id_id_key;
