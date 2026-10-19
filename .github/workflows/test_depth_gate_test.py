#!/usr/bin/env python3
"""Mutation proofs for the Photos test-depth gate."""

import unittest

import test_depth_gate as gate


class DepthGateMutationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = gate.WORKFLOW.read_text(encoding="utf-8")

    def test_planted_aggregate_masking_fails_per_package(self) -> None:
        floors = {"example/hot": 80.0, "example/cold": 80.0}
        measured = {"example/hot": 100.0, "example/cold": 70.0}
        self.assertGreater(sum(measured.values()) / len(measured), 80.0)
        self.assertIn(
            "example/cold coverage 70.0% is below 80.0%",
            gate.coverage_errors(floors, set(floors), measured),
        )

    def test_missing_race_execution_is_rejected(self) -> None:
        mutant = self.workflow.replace(f"run: {gate.RACE_COMMAND}", "run: go test ./...", 1)
        self.assertIn(
            "race job does not execute the required race command",
            gate.workflow_errors(mutant, gate.REQUIRED_FUZZ_TARGETS),
        )

    def test_required_fuzz_target_discovery_fails_closed(self) -> None:
        self.assertIn(
            "required fuzz target was not discovered: FuzzReadEXIF",
            gate.workflow_errors(self.workflow, set()),
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
