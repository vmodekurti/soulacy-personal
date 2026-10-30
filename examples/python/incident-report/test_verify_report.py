import unittest
from verify_report import verify_report

class ReportVerificationTests(unittest.TestCase):
    def test_missing_fields_fail_before_reporting_verification(self):
        fixtures = {
            'metrics-v2': {'baseline': [{'errors': 1, 'requests': 100}], 'incident': [{'errors': 3, 'requests': 100}]},
            'incident': {'id': 'test', 'owner': 'owner', 'start_utc': '2026-01-01T00:00:00Z', 'resolved_utc': '2026-01-01T01:00:00Z'},
            'impact-events': {'events': [{'event_id': 'a', 'tenant': 'one', 'affected_minutes': 60}]},
            'accounts': {'accounts': [{'tenant': 'one', 'monthly_fee': 100}]},
            'mitigation-options': {'actions': [{'id': 'flag', 'cost': 10, 'benefit': 5, 'requires': []}]},
        }
        report = {'incident_id': 'test', 'owner': 'owner', 'duration_minutes': 60, 'severity': 'SEV2',
                  'baseline_error_rate_pct': 1, 'incident_error_rate_pct': 3, 'increase_percentage_points': 2,
                  'error_rate_multiplier': 3, 'affected_tenants': ['one'], 'credits_usd': {'one': 10.0},
                  'total_credit_usd': 10, 'chosen_actions': ['flag'], 'action_cost_usd': 10,
                  'action_benefit_points': 5, 'budget_remaining_usd': 10, 'forbidden_actions': ['rollback'],
                  'root_cause_confidence': 'probable', 'external_actions_executed': False,
                  'source_ids': list(fixtures), 'customer_message_draft': 'Proposed message. ' * 5, 'rationale': 'Evidence supports the result. ' * 5}
        self.assertTrue(verify_report(report, fixtures, 20)['verified'])
        for field, wrong in [('error_rate_multiplier', None), ('error_rate_multiplier', 12),
                             ('action_cost_usd', 21), ('chosen_actions', ['rollback']), ('external_actions_executed', 0)]:
            with self.subTest(field=field, wrong=wrong), self.assertRaises(ValueError):
                verify_report(dict(report, **{field: wrong}), fixtures, 20)

if __name__ == '__main__':
    unittest.main()
