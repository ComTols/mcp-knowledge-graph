-- Replaces the former German default summary of the root document with the
-- English default used by 0001 for new installations. The old text is
-- matched by its MD5 hash, so a summary that was customized is never
-- overwritten. The change counts as a metadata change and bumps the version.
UPDATE documents
SET summary    = 'Top-level entry point of the knowledge base.',
    version    = version + 1,
    updated_at = now()
WHERE id = '00000000-0000-0000-0000-000000000001'
  AND md5(summary) = '9d981398af53c9b9169a55130fa04891';
