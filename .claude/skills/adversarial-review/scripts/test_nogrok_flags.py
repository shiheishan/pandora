#!/usr/bin/env python3
"""守卫：对抗审查脚本已改用 --composer / COMPOSER_SMALL，不留旧旗标双轨。"""
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[4]  # worktree 根
# 拼出已停用的旧参数名，避免 skill 全文检索误报
_LEGACY_FLAG = "--" + "gr" + "ok"
_LEGACY_SMALL = "GR" + "OK_SMALL"
_LEGACY_EXECUTOR = '"' + "gr" + "ok" + '"'
_LEGACY_ONLY = "<仅 " + "Gr" + "ok>"


class ComposerFlagsTest(unittest.TestCase):
    def test_triggers_source_uses_composer_only(self):
        text = (HERE / "triggers.py").read_text(encoding="utf-8")
        self.assertIn("--composer", text)
        self.assertNotIn(_LEGACY_FLAG, text)
        self.assertIn("COMPOSER_SMALL", text)
        self.assertNotIn(_LEGACY_SMALL, text)

    def test_review_prompt_accepts_composer_executor(self):
        text = (HERE / "review-prompt.py").read_text(encoding="utf-8")
        self.assertIn('"composer"', text)
        self.assertNotIn(_LEGACY_EXECUTOR, text)
        self.assertIn("<仅 Composer>", text)
        self.assertNotIn(_LEGACY_ONLY, text)

    def test_triggers_composer_runs(self):
        r = subprocess.run(
            [sys.executable, str(HERE / "triggers.py"), "feat/panel-redesign", "HEAD", "--composer", "-C", str(ROOT)],
            capture_output=True,
            text=True,
        )
        self.assertIn(r.returncode, (0, 1, 2))
        if r.returncode in (0, 1):
            self.assertIn("执行者 Composer", r.stdout)


if __name__ == "__main__":
    unittest.main()
