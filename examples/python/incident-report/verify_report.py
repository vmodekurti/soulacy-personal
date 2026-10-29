"""Deterministic verifier for the synthetic incident benchmark.

The fixture policy thresholds and forbidden actions are translated into code
below. This is a task-specific verifier, not a general factuality check.
"""
from datetime import datetime
from itertools import combinations
from math import isclose


def verify_report(report, fixtures, budget):
    metrics = fixtures['metrics-v2']
    rates = {period: 100 * sum(row['errors'] for row in metrics[period]) /
             sum(row['requests'] for row in metrics[period])
             for period in ('baseline', 'incident')}
    incident = fixtures['incident']
    duration = (datetime.fromisoformat(incident['resolved_utc'].replace('Z', '+00:00')) -
                datetime.fromisoformat(incident['start_utc'].replace('Z', '+00:00'))).total_seconds() / 60
    events = {event['event_id']: event for event in fixtures['impact-events']['events']}
    minutes = {}
    for event in events.values():
        minutes[event['tenant']] = minutes.get(event['tenant'], 0) + event['affected_minutes']
    fees = {account['tenant']: account['monthly_fee'] for account in fixtures['accounts']['accounts']}
    credits = {tenant: fees[tenant] * (0 if value < 30 else .05 if value < 60 else .1 if value < 120 else .2)
               for tenant, value in minutes.items()}
    # Explicit policy from deployment-evidence; propagate prohibited dependencies.
    actions = fixtures['mitigation-options']['actions']
    forbidden = {'rollback'}
    while True:
        expanded = forbidden | {action['id'] for action in actions if set(action['requires']) & forbidden}
        if expanded == forbidden:
            break
        forbidden = expanded
    feasible = []
    for n in range(len(actions) + 1):
        for subset in combinations(actions, n):
            ids = {action['id'] for action in subset}
            cost = sum(action['cost'] for action in subset)
            if cost > budget or ids & forbidden or any(not set(action['requires']) <= ids for action in subset):
                continue
            feasible.append((sum(action['benefit'] for action in subset), -cost, sorted(ids)))
    benefit, negative_cost, chosen = max(feasible)
    expected = {
        'incident_id': incident['id'], 'owner': incident['owner'], 'duration_minutes': duration,
        'severity': 'SEV1' if rates['incident'] >= 5 else 'SEV2' if rates['incident'] >= 1 else 'SEV3',
        'baseline_error_rate_pct': rates['baseline'], 'incident_error_rate_pct': rates['incident'],
        'increase_percentage_points': rates['incident'] - rates['baseline'],
        'error_rate_multiplier': rates['incident'] / rates['baseline'],
        'affected_tenants': sorted(minutes), 'credits_usd': credits, 'total_credit_usd': sum(credits.values()),
        'chosen_actions': chosen, 'action_cost_usd': -negative_cost, 'action_benefit_points': benefit,
        'budget_remaining_usd': budget + negative_cost, 'forbidden_actions': sorted(forbidden),
        'root_cause_confidence': 'probable', 'external_actions_executed': False,
    }
    errors = []
    for field, wanted in expected.items():
        value = report.get(field)
        if isinstance(wanted, (int, float)) and not isinstance(wanted, bool):
            valid = type(value) in (int, float) and isclose(value, wanted, abs_tol=1e-9, rel_tol=1e-9)
        else:
            valid = type(value) is type(wanted) and value == wanted
        if not valid:
            errors.append(f'{field}: missing or inconsistent with authoritative evidence and budget')
    aliases = {document.get('id'): name for name, document in fixtures.items()}
    sources = report.get('source_ids', [])
    if not isinstance(sources, list) or not all(isinstance(source, str) for source in sources) or not set(fixtures) <= {aliases.get(source, source) for source in sources}:
        errors.append('source_ids: incomplete provenance')
    for field, minimum in [('customer_message_draft', 40), ('rationale', 80)]:
        if not isinstance(report.get(field), str) or len(report[field].strip()) <= minimum:
            errors.append(f'{field}: substantive text required')
    if errors:
        raise ValueError('Report verification failed: ' + '; '.join(errors))
    return {'verified': True, 'checks': len(expected) + 3,
            'scope': 'required fields, evidence arithmetic, provenance, action constraints and optimality; prose presence only',
            'budget_usd': budget}
