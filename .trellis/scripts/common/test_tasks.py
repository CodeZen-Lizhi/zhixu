"""Direct tests for active-task child progress formatting."""

from __future__ import annotations

import unittest

from .tasks import children_progress


class ChildrenProgressTest(unittest.TestCase):
    def test_returns_empty_string_without_children(self) -> None:
        self.assertEqual(children_progress([], {}), "")

    def test_labels_progress_as_children_done(self) -> None:
        statuses = {
            "completed-child": "completed",
            "done-child": "done",
            "active-child": "in_progress",
        }

        self.assertEqual(
            children_progress(
                ["completed-child", "done-child", "active-child"], statuses
            ),
            " [2/3 done]",
        )

    def test_counts_missing_archived_child_as_done(self) -> None:
        self.assertEqual(
            children_progress(
                ("archived-child", "active-child"),
                {"active-child": "in_progress"},
            ),
            " [1/2 done]",
        )


if __name__ == "__main__":
    unittest.main()
