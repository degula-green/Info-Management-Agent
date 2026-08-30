-- Phase 1: email is the login identity; existing accounts remain intact.
ALTER TABLE identity.users ADD COLUMN IF NOT EXISTS email TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS users_email_unique_idx
    ON identity.users (lower(email))
    WHERE email IS NOT NULL AND btrim(email) <> '';
