-- Data-integrity invariants of a running index. Every row must report ok=t.
-- psql "$DATABASE_URL" -f scripts/check-invariants.sql
WITH l AS (SELECT min(sequence) AS lo, max(sequence) AS hi, count(*) AS n FROM ledgers),
gaps AS (SELECT coalesce(sum(to_ledger - from_ledger + 1), 0) AS missing FROM ledger_gaps
         WHERE from_ledger >= (SELECT lo FROM l))
SELECT 'ledgers contiguous (minus recorded gaps)' AS invariant,
       n = hi - lo + 1 - (SELECT missing FROM gaps) AS ok,
       format('%s ledgers in [%s, %s], %s in recorded gaps', n, lo, hi, (SELECT missing FROM gaps)) AS detail FROM l
UNION ALL
SELECT 'every ledger chains onto its predecessor',
       count(*) = 0, format('%s broken links', count(*))
FROM ledgers a JOIN ledgers b ON b.sequence = a.sequence - 1 WHERE a.prev_hash <> b.hash
UNION ALL
SELECT 'cursor = newest ledger',
       (SELECT ledger FROM cursors WHERE name = 'main') = (SELECT max(sequence) FROM ledgers),
       format('cursor %s, max ledger %s', (SELECT ledger FROM cursors WHERE name = 'main'), (SELECT max(sequence) FROM ledgers))
UNION ALL
SELECT 'no duplicate transactions', count(*) = count(DISTINCT hash), format('%s rows, %s distinct', count(*), count(DISTINCT hash))
FROM transactions
UNION ALL
SELECT 'ledger tx counts match stored txs (when failed txs are indexed)',
       coalesce(bool_and(l.tx_count = t.n), true), format('%s ledgers compared', count(*))
FROM ledgers l JOIN (SELECT ledger, count(*) AS n FROM transactions GROUP BY ledger) t ON t.ledger = l.sequence
UNION ALL
SELECT 'candles count each verified fill exactly once',
       (SELECT coalesce(sum(trades), 0) FROM candles_1m) = (SELECT count(*) FROM trades WHERE verified AND price > 0 AND base <> ''),
       format('candle fills %s, verified trades %s', (SELECT coalesce(sum(trades), 0) FROM candles_1m),
              (SELECT count(*) FROM trades WHERE verified AND price > 0 AND base <> ''));
