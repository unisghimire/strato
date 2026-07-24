-- Development seed data. Idempotent: safe to re-run.
--
-- NOTE: password_hash values below are syntactically valid Argon2id PHC
-- strings but are placeholders — these accounts cannot be logged into.
-- They exist to populate listings/relations for UI and query development.
-- For a login-able account, register through the API:
--   curl localhost:8080/v1/auth/register -d '{"email":"you@example.com","password":"a-strong-password"}'

INSERT INTO users (id, email, password_hash, display_name, role)
VALUES
  ('11111111-1111-1111-1111-111111111111', 'demo@strato.dev',
   '$argon2id$v=19$m=65536,t=3,p=2$KDhZUlpBc1M0T3BpRDFOSA$V2sm12u6PseKkSpUyc9BjZBoJ5S1osKMxpDbbeGgOSY',
   'Demo User', 'user'),
  ('22222222-2222-2222-2222-222222222222', 'admin@strato.dev',
   '$argon2id$v=19$m=65536,t=3,p=2$b1lHb2t4UmM5d0ZaU1BMWg$mBoBc8u6xkBqO0AqhK96f4uBnEeJ0S9wHnSKzeC5FyY',
   'Admin User', 'admin')
ON CONFLICT (email) DO NOTHING;

INSERT INTO storage_quotas (user_id, quota_bytes)
VALUES
  ('11111111-1111-1111-1111-111111111111', 10737418240),
  ('22222222-2222-2222-2222-222222222222', 107374182400)
ON CONFLICT (user_id) DO NOTHING;

INSERT INTO folders (id, owner_id, parent_id, name)
VALUES
  ('33333333-3333-3333-3333-333333333331', '11111111-1111-1111-1111-111111111111', NULL, 'Documents'),
  ('33333333-3333-3333-3333-333333333332', '11111111-1111-1111-1111-111111111111', NULL, 'Photos'),
  ('33333333-3333-3333-3333-333333333333', '11111111-1111-1111-1111-111111111111',
   '33333333-3333-3333-3333-333333333331', 'Taxes')
ON CONFLICT DO NOTHING;
