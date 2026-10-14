-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

-- 通用追加写守卫：挂在只追加的表上（BEFORE UPDATE OR DELETE OR TRUNCATE ... FOR EACH STATEMENT），挡应用误改；
-- 运行角色同时收回这些表的 UPDATE、DELETE、TRUNCATE 权限，挡直连的人。
-- +goose StatementBegin
CREATE FUNCTION deny_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% 是只追加的表，不允许 %', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

-- 通用禁删守卫：挂在允许改状态列、但不允许删除的表上（BEFORE DELETE）。
-- +goose StatementBegin
CREATE FUNCTION deny_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% 不允许删除', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS deny_delete();
DROP FUNCTION IF EXISTS deny_mutation();
-- citext 扩展不删：扩展是库级对象，可能被同库其他对象依赖，留着无害。
