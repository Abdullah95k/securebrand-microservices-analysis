-- Pilot data shape for the analysis design: COUNTS AND LENGTHS ONLY, never
-- content. Run on the pilot machine (docs/owner/analysis-decisions.md):
--   make pilot-stats
-- The output is safe to share: it holds no text, names or ids.
\pset footer off
\echo '== comments: totals, empties, tags only, edits, deletions'
SELECT count(*) AS comments,
       count(*) FILTER (WHERE depth > 0) AS replies,
       count(*) FILTER (WHERE text IS NULL OR btrim(text) = '') AS empty_text,
       count(*) FILTER (WHERE text IS NOT NULL AND text !~ '[A-Za-z0-9؀-ۿ٠-٩]' AND btrim(text) <> '') AS no_letters_emoji_or_symbols_only,
       count(*) FILTER (WHERE text_ranges IS NOT NULL AND (
           SELECT coalesce(sum((r->>'length')::int), 0) FROM jsonb_array_elements(text_ranges) r
           WHERE r->>'entity_type' = 'User') >= char_length(text) - 2) AS tags_only,
       count(*) FILTER (WHERE edit_count > 0) AS edited,
       count(*) FILTER (WHERE status = 'deleted') AS deleted,
       count(*) FILTER (WHERE attachments IS NOT NULL AND jsonb_array_length(attachments) > 0) AS with_attachment
FROM comments;

\echo '== comments: scripts and languages (by letters used)'
SELECT count(*) FILTER (WHERE text ~ '[؀-ۿ]') AS arabic_script,
       count(*) FILTER (WHERE text ~ '[ڕڵۆێە]') AS kurdish_sorani_letters,
       count(*) FILTER (WHERE text ~ '[çşêîûÇŞÊÎÛ]') AS kurmanji_or_turkish_letters,
       count(*) FILTER (WHERE text ~ '[پچژگڤ]') AS persian_letters_also_iraqi_dialect,
       count(*) FILTER (WHERE text ~ '[A-Za-z]' AND text !~ '[؀-ۿ]') AS latin_only,
       count(*) FILTER (WHERE text ~ '[A-Za-z]' AND text ~ '[؀-ۿ]') AS mixed_scripts,
       count(*) FILTER (WHERE text ~ '[A-Za-z][2357]|[2357][A-Za-z]' AND text !~ '[؀-ۿ]') AS arabizi_digits,
       count(*) FILTER (WHERE text ~ '[\U0001F300-\U0001FAFF☀-➿]') AS with_emoji
FROM comments WHERE text IS NOT NULL AND btrim(text) <> '';

\echo '== comments: length in characters and words (percentiles)'
SELECT percentile_cont(ARRAY[0.1, 0.25, 0.5, 0.75, 0.9, 0.99]) WITHIN GROUP (ORDER BY char_length(text)) AS chars_p10_25_50_75_90_99,
       percentile_cont(ARRAY[0.5, 0.9, 0.99]) WITHIN GROUP (ORDER BY array_length(regexp_split_to_array(btrim(text), '\s+'), 1)) AS words_p50_90_99,
       round(avg(char_length(text))) AS chars_avg
FROM comments WHERE text IS NOT NULL AND btrim(text) <> '';

\echo '== comments: exact duplicates (how many texts repeat, not which)'
SELECT count(*) AS non_empty, count(DISTINCT md5(lower(btrim(text)))) AS distinct_texts
FROM comments WHERE text IS NOT NULL AND btrim(text) <> '';
SELECT n AS copies, count(*) AS texts_with_that_many_copies
FROM (SELECT count(*) n FROM comments WHERE text IS NOT NULL AND btrim(text) <> '' GROUP BY md5(lower(btrim(text)))) t
GROUP BY n ORDER BY n DESC LIMIT 15;

\echo '== comments per post (percentiles) and per page per week'
SELECT percentile_cont(ARRAY[0.5, 0.9, 0.99]) WITHIN GROUP (ORDER BY n) AS comments_per_post_p50_90_99, max(n) AS max_per_post
FROM (SELECT post_id, count(*) n FROM comments GROUP BY post_id) t;
SELECT percentile_cont(ARRAY[0.1, 0.5, 0.9]) WITHIN GROUP (ORDER BY n) AS comments_per_page_p10_50_90
FROM (SELECT page_id, count(*) n FROM comments GROUP BY page_id) t;

\echo '== posts: types, lengths, media'
SELECT post_type, count(*) AS posts,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY char_length(coalesce(text, ''))) AS median_chars,
       round(avg(coalesce(array_length(media_ids, 1), 0)), 2) AS media_per_post
FROM posts GROUP BY post_type ORDER BY posts DESC;
SELECT count(*) FILTER (WHERE text ~ '[ڕڵۆێە]') AS posts_kurdish_letters,
       count(*) FILTER (WHERE text ~ '[A-Za-z]' AND text !~ '[؀-ۿ]') AS posts_latin_only,
       count(*) FILTER (WHERE coalesce(btrim(text), '') = '') AS posts_without_text
FROM posts;

\echo '== media discovered (the pilot downloads none)'
SELECT kind, count(*) FROM media GROUP BY kind ORDER BY 2 DESC;

\echo '== database size'
SELECT pg_size_pretty(pg_database_size(current_database())) AS database,
       (SELECT pg_size_pretty(sum(pg_total_relation_size(inhrelid))) FROM pg_inherits
        WHERE inhparent = 'comments'::regclass) AS comments_partitions;
