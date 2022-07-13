project('wit', version: 'development')
golang = find_program('go')
main = join_paths(meson.current_source_dir(), 'cmd/main.go')
vers = '-X main.version=' + meson.project_version()
flags = ['-ldflags'] + [vers] + ['-trimpath', '-buildmode=pie', '-mod=readonly', '-modcacherw']

in_files = run_command('find', meson.current_source_dir(), '-type', 'f', '-name', '*.go', check: true).stdout().strip().split()
in_files += 'go.mod'
in_files += 'go.sum'

wit = custom_target(
    'wit',
    output: 'wit',
    input: in_files,
    build_by_default: true,
    command: [ golang, 'build', flags, '-o','@OUTPUT@', main],
)

