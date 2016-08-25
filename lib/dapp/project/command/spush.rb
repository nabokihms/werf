module Dapp
  # Project
  class Project
    # Command
    module Command
      # Spush
      module Spush
        def spush(repo)
          raise Error::Project, code: :spush_command_unexpected_apps_number unless build_configs.one?
          Application.new(config: build_configs.first, project: self, ignore_git_fetch: true, should_be_built: true).tap do |app|
            app.export!(repo, format: '%{repo}:%{tag}')
          end
        end
      end
    end
  end # Project
end # Dapp
