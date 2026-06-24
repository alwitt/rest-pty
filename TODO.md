# TODOs

* In addition to direct `PTY` session driver, support docker as another driver implementation.
  * We can launch a docker container to run the session command.
  * This does add additional overhead, but the driver API should mostly be the same.
* With the exception of `api` and `app` package, all other packages need to convert `UseDatabaseInTransaction` calls to `ActiveSessionWrapper`.
