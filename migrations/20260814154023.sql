-- Create "sessions" table
CREATE TABLE "public"."sessions" (
  "id" text NOT NULL,
  "name" text NOT NULL,
  "description" text NULL,
  "command" bytea NOT NULL,
  "state" text NOT NULL,
  "driver" text NOT NULL,
  "driver_metadata" jsonb NULL,
  "workspace_name" text NULL,
  "io_buf_cap" bigint NOT NULL,
  "runner_mode" text NOT NULL,
  "created_at" timestamptz NULL,
  "updated_at" timestamptz NULL,
  CONSTRAINT "uni_sessions_id" PRIMARY KEY ("id"),
  CONSTRAINT "uni_sessions_name" UNIQUE ("name")
);
