-- Add 'active' state for user_steps.

ALTER TABLE user_steps
  DROP CONSTRAINT IF EXISTS user_steps_state_chk;

ALTER TABLE user_steps
  ADD CONSTRAINT user_steps_state_chk CHECK (state IN ('pending','active','completed','skipped'));
