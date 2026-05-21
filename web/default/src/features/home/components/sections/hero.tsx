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
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { HeroTerminalDemo } from '../hero-terminal-demo'

interface HeroProps {
  className?: string
  isAuthenticated?: boolean
}

export function Hero(props: HeroProps) {
  const { t } = useTranslation()

  return (
    <section className='relative z-10 flex flex-col items-center overflow-hidden px-6 pt-28 pb-16 md:pt-36 md:pb-24'>
      {/* Subtle dot grid background */}
      <div
        aria-hidden
        className='absolute inset-0 -z-10 bg-[radial-gradient(var(--border)_1px,transparent_1px)] [background-size:24px_24px] [mask-image:radial-gradient(ellipse_70%_60%_at_50%_30%,black_10%,transparent_100%)] opacity-[0.35]'
      />

      <div className='flex max-w-3xl flex-col items-center text-center'>
        <div
          className='landing-animate-fade-up mb-4 inline-flex items-center rounded-full border border-border/60 bg-muted/50 px-3 py-1 text-xs text-muted-foreground'
          style={{ animationDelay: '0ms' }}
        >
          <span className='mr-1.5 inline-block size-1.5 rounded-full bg-emerald-500' />
          {t('API Gateway Platform')}
        </div>
        <h1
          className='landing-animate-fade-up text-[clamp(2rem,5.5vw,3.5rem)] leading-[1.15] font-bold tracking-tight'
          style={{ animationDelay: '60ms' }}
        >
          {systemName}
          <br />
          <span className='text-muted-foreground font-normal'>
            {t('Your AI API, Managed.')}
          </span>
        </h1>
        <p
          className='landing-animate-fade-up text-muted-foreground mt-5 max-w-lg text-base leading-relaxed opacity-0 md:text-lg'
          style={{ animationDelay: '120ms' }}
        >
          {t(
            'Connect multiple upstream AI services, manage keys, quotas and routing — all in one place.'
          )}
        </p>
        <div
          className='landing-animate-fade-up mt-8 flex items-center gap-3 opacity-0'
          style={{ animationDelay: '200ms' }}
        >
          {props.isAuthenticated ? (
            <Button
              className='group rounded-md'
              render={<Link to='/dashboard' />}
            >
              {t('Go to Dashboard')}
              <ArrowRight className='ml-1 size-3.5 transition-transform duration-200 group-hover:translate-x-0.5' />
            </Button>
          ) : (
            <>
              <Button
                className='group rounded-md'
                render={<Link to='/sign-up' />}
              >
                {t('Get Started')}
                <ArrowRight className='ml-1 size-3.5 transition-transform duration-200 group-hover:translate-x-0.5' />
              </Button>
              <Button
                variant='outline'
                className='border-border hover:bg-muted/60 rounded-md'
                render={<Link to='/pricing' />}
              >
                {t('View Pricing')}
              </Button>
            </>
          )}
        </div>
      </div>

      <div
        className='landing-animate-fade-up w-full opacity-0'
        style={{ animationDelay: '320ms' }}
      >
        <HeroTerminalDemo />
      </div>
    </section>
  )
}
