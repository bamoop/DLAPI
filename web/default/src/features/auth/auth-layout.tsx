/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useSystemConfig } from '@/hooks/use-system-config'
import { Skeleton } from '@/components/ui/skeleton'

type AuthLayoutProps = {
  children: React.ReactNode
}

export function AuthLayout({ children }: AuthLayoutProps) {
  const { t } = useTranslation()
  const { systemName, logo, loading } = useSystemConfig()

  return (
    <div className='relative flex min-h-svh flex-col items-center justify-center bg-background px-4'>
      {/* Subtle dot pattern */}
      <div
        aria-hidden
        className='pointer-events-none absolute inset-0 bg-[radial-gradient(var(--border)_1px,transparent_1px)] [background-size:20px_20px] [mask-image:radial-gradient(ellipse_60%_60%_at_50%_50%,black_20%,transparent_100%)] opacity-40'
      />
      <div className='relative z-10 w-full max-w-[400px]'>
        {/* Logo / brand */}
        <div className='mb-8 flex items-center justify-center gap-2.5'>
          <Link
            to='/'
            className='flex items-center gap-2.5 transition-opacity hover:opacity-75'
          >
            <div className='relative size-8'>
              {loading ? (
                <Skeleton className='absolute inset-0 rounded-lg' />
              ) : (
                <img
                  src={logo}
                  alt={t('Logo')}
                  className='size-8 rounded-lg object-cover'
                />
              )}
            </div>
            {loading ? (
              <Skeleton className='h-5 w-20' />
            ) : (
              <span className='text-lg font-semibold tracking-tight'>
                {systemName}
              </span>
            )}
          </Link>
        </div>

        {/* Card */}
        <div className='rounded-xl border border-border bg-card px-8 py-8 shadow-sm'>
          {children}
        </div>
      </div>
    </div>
  )
}
