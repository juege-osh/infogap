"""Offline guard for the user's displayed weekly-quota budget; no network calls."""
from datetime import datetime, timezone, timedelta
from decimal import Decimal

CST = timezone(timedelta(hours=8))

class BudgetGuard:
    def __init__(self, baseline, day=None, max_points=5, reserve_points=1):
        self.baseline = dict(baseline)
        self.day = day or datetime.now(CST).date().isoformat()
        self.max_points = max_points
        self.reserve_points = reserve_points
        if not 0 < reserve_points < max_points:
            raise ValueError('必须预留额度缓冲')
        self._validate(baseline)

    @classmethod
    def from_record(cls, record, day=None):
        if day is not None and record.get('day') != day:
            raise ValueError('额度账本日期不一致')
        return cls(record['baseline'], day=day,
                   max_points=record.get('max_points', 5),
                   reserve_points=record.get('reserve_points', 1))

    @staticmethod
    def _validate(snapshot):
        used = snapshot.get('used_percent')
        if type(used) is not int or not 0 <= used <= 100:
            raise ValueError('官方周额度读数缺失或不正确')
        if type(snapshot.get('resets_at')) is not int:
            raise ValueError('周额度窗口缺失')
        if snapshot.get('window_minutes', 10080) != 10080:
            raise ValueError('必须使用每周额度窗口')

    def check(self, current, day=None):
        try:
            self._validate(current)
        except (ValueError, AttributeError, TypeError):
            return False, 'quota_unavailable'
        if (day or datetime.now(CST).date().isoformat()) != self.day:
            return False, 'day_changed_requires_new_daily_ledger'
        if abs(current['resets_at'] - self.baseline['resets_at']) > 5:
            return False, 'quota_window_changed'
        if current['used_percent'] < self.baseline['used_percent']:
            return False, 'quota_counter_went_backwards'
        if current.get('quota_reached') or current.get('allowed') is False:
            return False, 'official_quota_reached'
        before, after = self.baseline.get('credits'), current.get('credits')
        if before is None or after is None:
            return False, 'credit_reading_unavailable'
        if before is not None and after is not None:
            try:
                if Decimal(after) < Decimal(before):
                    return False, 'purchased_credit_balance_changed'
            except Exception:
                return False, 'credit_reading_invalid'
        used = current['used_percent'] - self.baseline['used_percent']
        if used >= self.max_points - self.reserve_points:
            return False, 'daily_display_budget_with_reserve'
        return True, 'may_dispatch_bounded_batch'

    def summary(self, current):
        allowed, reason = self.check(current)
        return {'may_dispatch': allowed, 'reason': reason,
                'baseline_display_used': self.baseline['used_percent'],
                'current_display_used': current.get('used_percent'),
                'maximum_daily_points': self.max_points,
                'reserve_points': self.reserve_points,
                'precision_note': '官方接口是整数刻度；这是批次派发保护，不能宣称精确控制小数百分点。'}
