"""Unit tests for PY-03 Ground Truth Oracle."""

import unittest
from studentdb_tests.oracle import Oracle, RecordState


class TestOracle(unittest.TestCase):
    def setUp(self):
        self.oracle = Oracle()

    def test_acked_record_present(self):
        tx_id = self.oracle.next_tx_id()
        data = {
            "first_name": "Asha",
            "last_name": "Rao",
            "date_of_birth": "2004-05-17",
            "gender": "female",
            "email": "asha.rao@example.com",
            "phone": "+91-9000000000",
            "department": "Computer Science",
            "enrollment_year": 2022,
        }
        self.oracle.stage_transaction(tx_id, [("insert", 20240001, data)])
        self.oracle.acknowledge_transaction(tx_id)

        actual = {20240001: data}
        ok, findings = self.oracle.compare(actual)
        self.assertTrue(ok)
        self.assertEqual(len(findings), 0)

    def test_acked_record_missing_flags_acked_loss(self):
        tx_id = self.oracle.next_tx_id()
        data = {"first_name": "Asha"}
        self.oracle.stage_transaction(tx_id, [("insert", 20240001, data)])
        self.oracle.acknowledge_transaction(tx_id)

        # Missing from DB
        actual = {}
        ok, findings = self.oracle.compare(actual)
        self.assertFalse(ok)
        self.assertTrue(any(f.verdict == "ACKED_LOSS" for f in findings))

    def test_never_sent_record_flags_phantom(self):
        self.oracle.record_never_sent(20240999)
        actual = {20240999: {"first_name": "Phantom"}}
        ok, findings = self.oracle.compare(actual)
        self.assertFalse(ok)
        self.assertTrue(any(f.verdict == "PHANTOM_RECORD" for f in findings))

    def test_in_doubt_all_present_passes(self):
        tx_id = self.oracle.next_tx_id()
        self.oracle.stage_transaction(tx_id, [
            ("insert", 20240010, {"first_name": "X"}),
            ("insert", 20240011, {"first_name": "Y"}),
        ])
        # Both present -> valid atomic commit
        actual = {
            20240010: {"first_name": "X"},
            20240011: {"first_name": "Y"},
        }
        ok, findings = self.oracle.compare(actual)
        self.assertTrue(ok)
        self.assertEqual(len(findings), 0)

    def test_in_doubt_all_absent_passes(self):
        tx_id = self.oracle.next_tx_id()
        self.oracle.stage_transaction(tx_id, [
            ("insert", 20240010, {"first_name": "X"}),
            ("insert", 20240011, {"first_name": "Y"}),
        ])
        # Both absent -> valid atomic rollback
        actual = {}
        ok, findings = self.oracle.compare(actual)
        self.assertTrue(ok)
        self.assertEqual(len(findings), 0)

    def test_in_doubt_partial_flags_partial_tx(self):
        tx_id = self.oracle.next_tx_id()
        self.oracle.stage_transaction(tx_id, [
            ("insert", 20240010, {"first_name": "X"}),
            ("insert", 20240011, {"first_name": "Y"}),
        ])
        # Only one present -> violation of all-or-nothing atomicity
        actual = {20240010: {"first_name": "X"}}
        ok, findings = self.oracle.compare(actual)
        self.assertFalse(ok)
        self.assertTrue(any(f.verdict == "PARTIAL_TX" for f in findings))


if __name__ == "__main__":
    unittest.main()

