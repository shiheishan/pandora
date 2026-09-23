import { useDraft } from "./drafts";
import { recordSchema, type Row } from "./data";

// Only callers with an explicit non-secret field set may persist a form.
export function useObjectDraft(subject: string, entity: string) {
  const draft = useDraft(subject, entity);
  let value: Row = {};
  try {
    const parsed = recordSchema.safeParse(JSON.parse(draft.value || "{}"));
    if (parsed.success) value = parsed.data;
  } catch {
    /* Ignore malformed form drafts. */
  }
  return {
    ...draft,
    value,
    setValue: (value: Row) => draft.setValue(JSON.stringify(value)),
  };
}
