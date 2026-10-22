import unittest
from budget import BudgetGuard

class BudgetTests(unittest.TestCase):
 def setUp(self):
  self.base={'used_percent':67,'resets_at':1793082756,'window_minutes':10080,'credits':'30839.120634','allowed':True}
  self.guard=BudgetGuard(self.base,day='2026-10-22')
 def check(self,**changes):
  return self.guard.check(dict(self.base,**changes),day='2026-10-22')
 def test_reserve_stops_before_five_points(self):
  self.assertTrue(self.check(used_percent=70)[0])
  self.assertEqual(self.check(used_percent=71),(False,'daily_display_budget_with_reserve'))
  self.assertFalse(self.check(used_percent=72)[0])
 def test_missing_or_wrong_quota_cannot_launch(self):
  for value in [None,67.0,'67',True,-1,101]:
   self.assertEqual(self.check(used_percent=value),(False,'quota_unavailable'))
  self.assertFalse(self.check(window_minutes=300)[0])
 def test_window_rounding_and_reset(self):
  self.assertTrue(self.check(resets_at=self.base['resets_at']+1)[0])
  self.assertFalse(self.check(resets_at=self.base['resets_at']+3600)[0])
  self.assertFalse(self.check(used_percent=66)[0])
 def test_no_automatic_paid_credit_fallback(self):
  self.assertEqual(self.check(credits='30838.120634'),(False,'purchased_credit_balance_changed'))
  self.assertFalse(self.check(allowed=False)[0])
  self.assertFalse(self.check(quota_reached=True)[0])
 def test_missing_credit_meter_stops_dispatch(self):
  self.assertEqual(self.check(credits=None),(False,'credit_reading_unavailable'))
 def test_new_day_requires_explicit_daily_ledger(self):
  self.assertEqual(self.guard.check(self.base,day='2026-10-23'),(False,'day_changed_requires_new_daily_ledger'))
 def test_explicit_daily_budget_extension_keeps_original_baseline(self):
  record={'day':'2026-10-22','baseline':self.base,'max_points':10,'reserve_points':1}
  guard=BudgetGuard.from_record(record,day='2026-10-22')
  self.assertEqual(guard.baseline['used_percent'],67)
  self.assertTrue(guard.check(dict(self.base,used_percent=75),day='2026-10-22')[0])
  self.assertFalse(guard.check(dict(self.base,used_percent=76),day='2026-10-22')[0])
  with self.assertRaises(ValueError):BudgetGuard.from_record(record,day='2026-10-23')
if __name__=='__main__':unittest.main()
