import { ChevronDown, Monitor, Moon, Sun } from "lucide-react";
import { DropdownMenu as DropdownMenuPrimitive } from "radix-ui";
import type { ReactNode } from "react";

import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useCurrentUser } from "@/hooks/useCurrentUser";
import { isThemeMode, type ThemeMode } from "@/hooks/useThemePreference";
import { userInitials } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useTheme } from "@/providers/ThemeProvider";

export function Topbar() {
  const { data: user } = useCurrentUser();
  const { themeMode, setThemeMode } = useTheme();

  const displayName = user?.name || user?.username || user?.email || "Account";

  return (
    <header className="flex h-[60px] w-full items-center justify-between border-border border-b bg-background px-10">
      <a href="/" className="flex items-center" aria-label="Go to homepage">
        <img src="/nebari-logo.svg" alt="Nebari" className="h-8 w-auto" />
      </a>

      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label="Account menu"
            className="flex items-center gap-3 rounded-md px-1 py-1 transition-none hover:bg-accent focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            <Avatar className="h-8 w-8">
              <AvatarFallback className="bg-primary font-semibold text-primary-foreground text-sm">
                {userInitials(user?.name || user?.username, user?.email)}
              </AvatarFallback>
            </Avatar>
            <span className="font-medium text-foreground text-sm">{displayName}</span>
            <ChevronDown className="h-4 w-4 text-muted-foreground" />
          </button>
        </DropdownMenuTrigger>

        <DropdownMenuContent align="end" className="w-72">
          <div className="border-b px-3 py-2">
            <p className="font-medium text-foreground text-sm">
              {user?.name || user?.username || "Signed in"}
            </p>
            {user?.email ? <p className="text-muted-foreground text-xs">{user.email}</p> : null}
          </div>

          <div className="px-2 py-2">
            <DropdownMenuRadioGroup
              aria-label="Theme"
              value={themeMode}
              onValueChange={(value) => {
                if (isThemeMode(value)) setThemeMode(value);
              }}
              className="flex items-center gap-1 rounded-lg bg-muted p-1"
            >
              <ThemeOption value="light" label="Light mode" text="Light">
                <Sun className="h-4 w-4" />
              </ThemeOption>
              <ThemeOption value="dark" label="Dark mode" text="Dark">
                <Moon className="h-4 w-4" />
              </ThemeOption>
              <ThemeOption value="system" label="System theme" text="System">
                <Monitor className="h-4 w-4" />
              </ThemeOption>
            </DropdownMenuRadioGroup>
          </div>

          <DropdownMenuSeparator />

          <DropdownMenuItem asChild className="cursor-pointer">
            <a href="/logout">Sign out</a>
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </header>
  );
}

function ThemeOption({
  value,
  label,
  text,
  children,
}: {
  value: ThemeMode;
  label: string;
  text: string;
  children: ReactNode;
}) {
  return (
    <DropdownMenuPrimitive.RadioItem
      value={value}
      aria-label={label}
      title={label}
      // Keep the menu open after switching themes so the change is visible.
      onSelect={(event) => event.preventDefault()}
      className={cn(
        "flex flex-1 cursor-pointer items-center justify-center gap-1.5 rounded-md px-2 py-1.5 text-sm outline-none transition-colors focus-visible:ring-[3px] focus-visible:ring-ring/50",
        "text-muted-foreground hover:text-foreground",
        "data-[state=checked]:bg-background data-[state=checked]:text-foreground data-[state=checked]:shadow-sm",
      )}
    >
      {children}
      <span>{text}</span>
    </DropdownMenuPrimitive.RadioItem>
  );
}
