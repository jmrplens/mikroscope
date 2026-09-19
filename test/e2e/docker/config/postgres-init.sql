-- The second database the Postgres sink writes to.
--
-- The SQL sink's script is loaded into `mikroscope`, and the connecting sink
-- writes here, so the two can be compared row for row instead of merging into
-- one set of tables that ON CONFLICT DO NOTHING would make agree for the wrong
-- reason. Created at init because pgxpool connects to a database, it does not
-- make one.
CREATE DATABASE mikroscope_direct;
