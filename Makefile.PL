use ExtUtils::MakeMaker;
use ExtUtils::MY;

sub MY::postamble {
    return <<'MAKE_GOTOOLS';
gotools:
	cd src/go && $(MAKE) build

MAKE_GOTOOLS
}

WriteMakefile(
    NAME      => 'Percona::Toolkit',
    VERSION   => '3.5.7',
    EXE_FILES => [
      map {
         (my $name = $_) =~ s/^bin.//;
         my $file_name = $_;
         if ( ( $file_name !~ m/mongo/ ) || ( $file_name !~ m/pg/ ) || ( $file_name !~ m/pt-stalk/ )  || ( $file_name !~ m/pt-k8s/ )  ) {
	     $_;
         }
      } <bin/*>
    ],
    MAN1PODS  => {
      'docs/percona-toolkit.pod' => 'blib/man1/percona-toolkit.1p',
      map {
         (my $name = $_) =~ s/^bin.//;
         my $file_name = $_;
         if ( ( $file_name !~ m/mongo/ ) || ( $file_name !~ m/pg/ ) || ( $file_name !~ m/pt-stalk/ )  || ( $file_name !~ m/pt-k8s/ )  ) {
             $_ => "blib/man1/$name.1p";
         }
      } <bin/*>
    },
    MAN3PODS     => {}, # man(3) pages are for C libs
    depend => {manifypods => gotools},
    PREREQ_PM    => {
        DBI           => 1.46,
        DBD::mysql    => 3.0000_0,
    },
);